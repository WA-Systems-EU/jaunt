package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/WA-Systems-EU/jaunt/internal/agentrun"
	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/forge"
	"github.com/WA-Systems-EU/jaunt/internal/minter"
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
	"github.com/WA-Systems-EU/jaunt/internal/supervisor"
	"github.com/WA-Systems-EU/jaunt/internal/vault"
)

// maxSecretsInput bounds the bundle `secrets-load` accepts on stdin.
const maxSecretsInput = 1 << 20

// boxOnlyCmds are box commands with no laptop transition alias.
func boxOnlyCmds() []*cobra.Command {
	load := &cobra.Command{
		Use:   "secrets-load",
		Short: "Load the secret bundle sent on stdin into the box's RAM (box side, root)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return errors.New("run with sudo")
			}
			return loadSecretsFrom(cmd.InOrStdin(), vault.Dir)
		},
	}
	mint := &cobra.Command{
		Use:   "mint <owner/repo>",
		Short: "Print a single-repository GitHub token as JSON (box side, root; used by the git credential helper)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The argument comes from a possibly hostile sudo caller: check it
			// before anything else, independent of forge or vault state.
			if err := validateRepoArg(args[0]); err != nil {
				return err
			}
			if os.Geteuid() != 0 {
				return errors.New("run with sudo")
			}
			tok, err := newMinter().Mint(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(mintOutput{Token: tok.Value, ExpiresAt: tok.ExpiresAt.UTC().Format(time.RFC3339)})
		},
	}
	var asJSON bool
	repos := &cobra.Command{
		Use:   "repos",
		Short: "List the repositories the GitHub App is installed on (box side, root)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return errors.New("run with sudo")
			}
			rs, err := newMinter().Repos(cmd.Context())
			if err != nil {
				return err
			}
			return printRepos(cmd.OutOrStdout(), rs, asJSON)
		},
	}
	repos.Flags().BoolVar(&asJSON, "json", false, "print a JSON array")
	cred := &cobra.Command{
		Use:       "git-credential get|store|erase",
		Short:     "Git credential helper: mints a repo-scoped token via sudo (box side, owner)",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"get", "store", "erase"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return gitCredential(args[0], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), sudoMint)
		},
	}
	cred.SilenceErrors = true
	agentNet := &cobra.Command{
		Use:    "agent-net",
		Short:  "Block the metadata service, the tailnet and the host from agent containers (box side, root)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return errors.New("run with sudo")
			}
			return runAgentNet(cmd.Context(), agentrun.HostRunner, boxInfoPath)
		},
	}
	return []*cobra.Command{load, mint, repos, cred, agentNet}
}

// boxInfoPath is where agent-net reads the box-wide host ports; tests point it
// at a temp file.
var boxInfoPath = boxinfo.Path

// runAgentNet applies the network guard with the box-wide host ports from
// box.json. A missing file means no ports; an unreadable or invalid one still
// applies the guard with no ports, then returns the error.
func runAgentNet(ctx context.Context, run agentrun.Runner, path string) error {
	info, err := boxinfo.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agentrun.EnsureNetGuard(ctx, run, nil)
		}
		if gerr := agentrun.EnsureNetGuard(ctx, run, nil); gerr != nil {
			return gerr
		}
		return err
	}
	if err := config.CheckHostPorts("agent.host_ports", info.Agent.HostPorts); err != nil {
		if gerr := agentrun.EnsureNetGuard(ctx, run, nil); gerr != nil {
			return gerr
		}
		return err
	}
	return agentrun.EnsureNetGuard(ctx, run, info.Agent.HostPorts)
}

var repoArgRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// validateRepoArg accepts only owner/name made of [A-Za-z0-9_.-], with neither
// part "." or "..".
func validateRepoArg(s string) error {
	bad := fmt.Errorf("invalid repository %q: want owner/name", s)
	if !repoArgRE.MatchString(s) {
		return bad
	}
	for _, part := range strings.Split(s, "/") {
		if part == "." || part == ".." {
			return bad
		}
	}
	return nil
}

func newMinter() *minter.Minter {
	return minter.New(events.JSONLFile(supervisor.DefaultPaths().Events))
}

type mintOutput struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type repoOutput struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
}

func printRepos(w io.Writer, rs []forge.Repo, asJSON bool) error {
	if asJSON {
		out := make([]repoOutput, 0, len(rs))
		for _, r := range rs {
			out = append(out, repoOutput{r.Owner, r.Name, r.DefaultBranch, r.CloneURL})
		}
		return json.NewEncoder(w).Encode(out)
	}
	for _, r := range rs {
		fmt.Fprintf(w, "%s  (default branch %s)\n", r.FullName(), r.DefaultBranch)
	}
	return nil
}

// sudoMint asks the root-only `mint` command for a token. Its stderr line
// (locked, not set up, not installed) becomes the error verbatim.
func sudoMint(repo string) (forge.Token, error) {
	c := exec.Command("sudo", "-n", "/usr/local/bin/jauntctl", "mint", repo)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		msg = strings.TrimPrefix(msg, BoxName+": ")
		if msg == "" {
			msg = "could not mint a token: " + err.Error()
		}
		return forge.Token{}, errors.New(msg)
	}
	var o mintOutput
	if err := json.Unmarshal(stdout.Bytes(), &o); err != nil || o.Token == "" {
		return forge.Token{}, errors.New("mint returned no token")
	}
	exp, _ := time.Parse(time.RFC3339, o.ExpiresAt)
	return forge.Token{Value: o.Token, ExpiresAt: exp}, nil
}

// gitCredential implements the git credential helper protocol. mint is
// injectable so tests need no sudo.
func gitCredential(op string, in io.Reader, out, errw io.Writer, mint func(repo string) (forge.Token, error)) error {
	kv := map[string]string{}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			kv[k] = v
		}
	}
	switch op {
	case "store", "erase":
		return nil
	case "get":
	default:
		return fmt.Errorf("unknown git-credential operation %q", op)
	}
	if kv["host"] != "github.com" || kv["path"] == "" {
		return nil
	}
	repo := strings.TrimSuffix(strings.Trim(kv["path"], "/"), ".git")
	if strings.Count(repo, "/") != 1 {
		return nil
	}
	tok, err := mint(repo)
	if err != nil {
		fmt.Fprintln(errw, err.Error())
		// Without quit=1 git ignores the failure and prompts for a username.
		fmt.Fprint(out, "quit=1\n")
		return errCredential
	}
	fmt.Fprintf(out, "username=x-access-token\npassword=%s\n", tok.Value)
	return nil
}

// errCredential makes git-credential exit 1 after it already printed its line.
var errCredential = errors.New("git-credential failed")

// loadSecretsFrom is loadSecretsWith with the real box user and chpasswd.
func loadSecretsFrom(r io.Reader, dir string) error {
	return loadSecretsWith(r, dir, boxUser, runChpasswd)
}

// boxUser is the box user's name from the box description.
func boxUser() (string, error) {
	info, err := boxinfo.Read(boxinfo.Path)
	if err != nil {
		return "", err
	}
	if info.User == "" {
		return "", errors.New("box description has no user")
	}
	return info.User, nil
}

// runChpasswd feeds "<user>:<password>\n" to chpasswd. Its stderr is dropped
// and only the exit status is reported, because chpasswd may echo the input.
func runChpasswd(stdin []byte) error {
	cmd := exec.Command("/usr/sbin/chpasswd")
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Run()
}

// loadSecretsWith reads one JSON bundle (at most maxSecretsInput bytes) and
// loads it into dir. BOX_SUDO_PASSWORD, if present in agents.env, is removed
// from what is stored and applied with chpasswd instead; if that fails the
// other secrets are still loaded and the failure is returned afterwards.
// Errors never include the input.
func loadSecretsWith(r io.Reader, dir string, user func() (string, error), chpasswd func(stdin []byte) error) error {
	data, err := io.ReadAll(io.LimitReader(r, maxSecretsInput+1))
	if err != nil {
		return fmt.Errorf("read secrets: %w", err)
	}
	if len(data) > maxSecretsInput {
		return fmt.Errorf("secret bundle too large (limit %d bytes)", maxSecretsInput)
	}
	var b secrets.Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return errors.New("secret bundle is not valid JSON")
	}
	var pw string
	var havePW bool
	if env, ok := b.Files[secrets.AgentEnvFile]; ok {
		var rest []byte
		pw, havePW, rest = secrets.ExtractEnv(env, secrets.BoxSudoPassword)
		if havePW {
			if len(bytes.TrimSpace(rest)) == 0 {
				delete(b.Files, secrets.AgentEnvFile)
			} else {
				b.Files[secrets.AgentEnvFile] = rest
			}
		}
	}
	if err := vault.Load(dir, b); err != nil {
		return err
	}
	if !havePW {
		return nil
	}
	const failMsg = "setting the box user's sudo password failed"
	if pw == "" || strings.ContainsAny(pw, "\r\n\x00") {
		return errors.New(failMsg + " (the stored password is empty or not a single line)")
	}
	name, err := user()
	if err != nil {
		return fmt.Errorf("%s (box user unknown: %v)", failMsg, err)
	}
	if err := chpasswd([]byte(name + ":" + pw + "\n")); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("%s (chpasswd exit %d)", failMsg, ee.ExitCode())
		}
		return errors.New(failMsg + " (chpasswd could not run)")
	}
	return nil
}
