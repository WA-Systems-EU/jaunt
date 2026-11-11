package client

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ErrRootSSH is matched (errors.Is) by a Deps.Root failure that looks like a
// refused or unreachable root login: ssh's own exit status 255 or an error
// with no exit status at all, and every Copy error. Any other remote exit
// status is reported as a command failure, not ErrRootSSH. Root steps are never retried through Deps.Remote (the box
// user has no sudo for them), so the only remedy is the tailnet policy.
var ErrRootSSH = errors.New("root ssh failed")

type rootSSHError struct {
	host, user string
	err        error
}

func (e *rootSSHError) Error() string {
	return fmt.Sprintf("root ssh to %s failed; your tailnet policy must allow ssh as root to tag:jauntbox-%s: run `jauntbox acl-snippet` and update the policy: %v", e.host, e.user, e.err)
}
func (e *rootSSHError) Unwrap() error        { return e.err }
func (e *rootSSHError) Is(target error) bool { return target == ErrRootSSH }

// rootErr wraps err as ErrRootSSH (nil stays nil) and keeps the underlying
// error in the chain. Run/RunInput go through rootCmdErr, which only wraps
// the 255/no-exit-status cases; rootCopy wraps every error because a failed
// copy carries no remote exit status to tell the cases apart.
func rootErr(d Deps, err error) error {
	if err == nil {
		return nil
	}
	return &rootSSHError{host: hostname(d.Cfg.User), user: d.Cfg.User, err: err}
}

// rootCmdErr classifies a Run/RunInput error: ssh failing to connect or log in
// (no exit status, or ssh's own 255) is a root ssh problem; any other exit
// status comes from the remote command and is reported as such.
func rootCmdErr(d Deps, err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() != 255 {
		return fmt.Errorf("on the box: %w", err)
	}
	return rootErr(d, err)
}

func rootRun(ctx context.Context, d Deps, args ...string) ([]byte, error) {
	out, err := d.Root.Run(ctx, hostname(d.Cfg.User), args...)
	return out, rootCmdErr(d, err)
}

func rootRunInput(ctx context.Context, d Deps, stdin []byte, args ...string) ([]byte, error) {
	out, err := d.Root.RunInput(ctx, hostname(d.Cfg.User), stdin, args...)
	return out, rootCmdErr(d, err)
}

func rootCopy(ctx context.Context, d Deps, local, remotePath string) error {
	return rootErr(d, d.Root.Copy(ctx, hostname(d.Cfg.User), local, remotePath))
}
