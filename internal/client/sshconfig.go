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
// content, and creates the directory (0700) and file (0600) when missing.
func EnsureSSHConfig(path, user string) (added bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	host := "jauntbox-" + user
	for _, line := range strings.Split(string(existing), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.EqualFold(f[0], "Host") {
			for _, pat := range f[1:] {
				if pat == host {
					return false, nil
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
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
		return false, fmt.Errorf("append to %s: %w", path, err)
	}
	return true, f.Close()
}
