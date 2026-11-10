package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SSHConfigBlock is the ~/.ssh/config block that lets `herdr --remote` (which
// cannot take ssh options) reach the box: the peer is authenticated by
// Tailscale and every recreated box has new host keys.
func SSHConfigBlock(user string) string {
	return "# added by jauntbox init\nHost jauntbox-" + user + "\n    StrictHostKeyChecking no\n    UserKnownHostsFile /dev/null\n    LogLevel ERROR\n"
}

// EnsureSSHConfig appends SSHConfigBlock to the ssh config at path unless a
// `Host jauntbox-<user>` line is already there. It never rewrites existing
// content (a bare existing entry yields a warning naming the options it lacks),
// and creates the directory (0700) and file (0600) when missing.
func EnsureSSHConfig(path, user string) (added bool, warning string, err error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, "", err
	}
	host := "jauntbox-" + user
	lines := strings.Split(string(existing), "\n")
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.EqualFold(f[0], "Host") {
			for _, pat := range f[1:] {
				if pat == host {
					return false, missingOptions(lines[i+1:], path, host), nil
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, "", err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, "", err
	}
	defer f.Close()
	prefix := ""
	if len(existing) > 0 {
		prefix = "\n"
		if !strings.HasSuffix(string(existing), "\n") {
			prefix = "\n\n"
		}
	}
	if _, err := f.WriteString(prefix + SSHConfigBlock(user)); err != nil {
		return false, "", fmt.Errorf("append to %s: %w", path, err)
	}
	return true, "", f.Close()
}

// missingOptions inspects the options of the Host block that starts just before
// rest and returns a warning when StrictHostKeyChecking or UserKnownHostsFile
// is not set there ("" when both are).
func missingOptions(rest []string, path, host string) string {
	have := map[string]bool{}
	for _, line := range rest {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if k := strings.ToLower(f[0]); k == "host" || k == "match" {
			break
		}
		have[strings.ToLower(strings.SplitN(f[0], "=", 2)[0])] = true
	}
	var missing []string
	for _, o := range []string{"StrictHostKeyChecking", "UserKnownHostsFile"} {
		if !have[strings.ToLower(o)] {
			missing = append(missing, o)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("%s has a Host %s entry without %s; `herdr --remote` will prompt about the box's host key (every recreated box has new keys). Add them yourself:\n\n%s", path, host, strings.Join(missing, " and "), SSHConfigBlock(strings.TrimPrefix(host, "jauntbox-")))
}
