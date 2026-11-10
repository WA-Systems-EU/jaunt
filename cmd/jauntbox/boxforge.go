package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
	return []*cobra.Command{load, mint, repos, cred}
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
		return errCredential
	}
	fmt.Fprintf(out, "username=x-access-token\npassword=%s\n", tok.Value)
	return nil
}

// errCredential makes git-credential exit 1 after it already printed its line.
var errCredential = errors.New("git-credential failed")

// loadSecretsFrom reads one JSON bundle (at most maxSecretsInput bytes) and
// loads it into dir. Errors never include the input.
func loadSecretsFrom(r io.Reader, dir string) error {
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
	return vault.Load(dir, b)
}
