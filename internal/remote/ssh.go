// Package remote runs commands on and copies files to the jauntbox over ssh.
package remote

import (
	"context"
	"fmt"
	"os/exec"
)

// Runner executes commands on the box.
type Runner interface {
	// Run executes `ssh <user>@<host> -- args` and returns combined output.
	Run(ctx context.Context, host string, args ...string) ([]byte, error)
	// Copy uploads localPath to remotePath with scp.
	Copy(ctx context.Context, host, localPath, remotePath string) error
}

type sshRunner struct{ user string }

// NewSSH returns a Runner that shells out to ssh/scp as the given remote user.
func NewSSH(user string) Runner { return sshRunner{user: user} }

// HostKeyOpts disables host-key checking: the peer is authenticated by
// Tailscale (WireGuard + ACL tag), and every recreated box has new host keys.
var HostKeyOpts = []string{"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR"}

var baseOpts = append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}, HostKeyOpts...)

func (s sshRunner) target(host string) string { return s.user + "@" + host }

func (s sshRunner) Run(ctx context.Context, host string, args ...string) ([]byte, error) {
	argv := append(append([]string{}, baseOpts...), s.target(host), "--")
	argv = append(argv, args...)
	out, err := exec.CommandContext(ctx, "ssh", argv...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("ssh %s: %w: %s", s.target(host), err, out)
	}
	return out, nil
}

func (s sshRunner) Copy(ctx context.Context, host, localPath, remotePath string) error {
	argv := append(append([]string{}, baseOpts...), localPath, s.target(host)+":"+remotePath)
	if out, err := exec.CommandContext(ctx, "scp", argv...).CombinedOutput(); err != nil {
		return fmt.Errorf("scp to %s: %w: %s", s.target(host), err, out)
	}
	return nil
}
