package session

import (
	"encoding/json"
	"fmt"

	"github.com/WA-Systems-EU/jaunt/internal/remote"
)

// Herdr is the herdr (https://herdr.dev) session adapter. The workspace
// subcommands have no --session flag; they address a named session through
// the HERDR_SESSION environment variable.
type Herdr struct{}

// remoteHerdr is herdr's absolute path on the box: a non-interactive ssh
// command reads no shell rc file, so ~/.local/bin is not on PATH there.
func remoteHerdr(user string) string { return "/home/" + user + "/.local/bin/herdr" }

func (Herdr) AttachCommand(host, user, name string, localHerdr bool) []string {
	target := user + "@" + host
	if localHerdr {
		return []string{"herdr", "--remote", target, "--session", name}
	}
	argv := []string{"ssh", "-t", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}
	argv = append(argv, remote.HostKeyOpts...)
	return append(argv, target, "--", remoteHerdr(user), "--session", name)
}

func (Herdr) StartCommand(user, name string) []string {
	// One shell string (ssh joins argv with spaces): start the headless server
	// detached, with stdio closed so ssh does not wait for it.
	return []string{fmt.Sprintf(
		"HERDR_SESSION=%[1]s %[2]s workspace list >/dev/null 2>&1 || { HERDR_SESSION=%[1]s setsid -f %[2]s server </dev/null >/dev/null 2>&1; sleep 2; }",
		name, remoteHerdr(user))}
}

func (Herdr) ListCommand(user, name string) []string {
	return []string{"env", "HERDR_SESSION=" + name, remoteHerdr(user), "workspace", "list"}
}

func (Herdr) EnsureWorkspaces(name, user string, projects []string, listOutput []byte) ([][]string, error) {
	var parsed struct {
		Result struct {
			Workspaces []struct {
				Label string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listOutput, &parsed); err != nil {
		return nil, fmt.Errorf("parse `herdr workspace list` output: %w", err)
	}
	have := map[string]bool{}
	for _, w := range parsed.Result.Workspaces {
		have[w.Label] = true
	}
	var cmds [][]string
	for _, p := range projects {
		if have[p] {
			continue
		}
		cmds = append(cmds, []string{"env", "HERDR_SESSION=" + name, remoteHerdr(user), "workspace", "create",
			"--cwd", "/home/" + user + "/src/" + p, "--label", p, "--no-focus"})
	}
	return cmds, nil
}
