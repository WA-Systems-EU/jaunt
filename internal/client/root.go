package client

import (
	"context"
	"fmt"
)

// ErrRootSSH is matched (errors.Is) by every error that comes back from a
// Deps.Root call. Root steps are never retried through Deps.Remote (the box
// user has no sudo for them), so the only remedy is the tailnet policy.
var ErrRootSSH = fmt.Errorf("root ssh failed")

type rootSSHError struct {
	host, user string
	err        error
}

func (e *rootSSHError) Error() string {
	return fmt.Sprintf("root ssh to %s failed; your tailnet policy must allow ssh as root to tag:jauntbox-%s: run `jauntbox acl-snippet` and update the policy: %v", e.host, e.user, e.err)
}
func (e *rootSSHError) Unwrap() error        { return e.err }
func (e *rootSSHError) Is(target error) bool { return target == ErrRootSSH }

// rootErr wraps err (nil stays nil). The Runner interface cannot tell a
// refused root login from a failing remote command, so every error is wrapped
// and the underlying error stays in the chain.
func rootErr(d Deps, err error) error {
	if err == nil {
		return nil
	}
	return &rootSSHError{host: hostname(d.Cfg.User), user: d.Cfg.User, err: err}
}

func rootRun(ctx context.Context, d Deps, args ...string) ([]byte, error) {
	out, err := d.Root.Run(ctx, hostname(d.Cfg.User), args...)
	return out, rootErr(d, err)
}

func rootRunInput(ctx context.Context, d Deps, stdin []byte, args ...string) ([]byte, error) {
	out, err := d.Root.RunInput(ctx, hostname(d.Cfg.User), stdin, args...)
	return out, rootErr(d, err)
}

func rootCopy(ctx context.Context, d Deps, local, remotePath string) error {
	return rootErr(d, d.Root.Copy(ctx, hostname(d.Cfg.User), local, remotePath))
}
