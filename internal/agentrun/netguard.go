// Package agentrun runs agent containers on the box (root side).
package agentrun

import "context"

// Runner executes an external command; tests substitute a fake.
type Runner func(ctx context.Context, name string, args ...string) error

type guardRule struct {
	bin  string
	args []string
}

// guardRules are what agent containers (root Docker daemon, so the per-uid
// metadata guard does not cover them) must never reach: the Hetzner metadata
// service and the tailnet (CGNAT range and the Tailscale ULA prefix).
var guardRules = []guardRule{
	{"iptables", []string{"-d", "169.254.169.254/32", "-j", "DROP"}},
	{"iptables", []string{"-d", "100.64.0.0/10", "-j", "DROP"}},
	{"ip6tables", []string{"-d", "fd7a:115c:a1e0::/48", "-j", "DROP"}},
}

// EnsureNetGuard makes sure each DROP rule is in DOCKER-USER, inserting the
// missing ones at the top. An insert failure is returned: callers fail closed.
func EnsureNetGuard(ctx context.Context, run Runner) error {
	for _, r := range guardRules {
		check := append([]string{"-C", "DOCKER-USER"}, r.args...)
		if run(ctx, r.bin, check...) == nil {
			continue
		}
		ins := append([]string{"-I", "DOCKER-USER", "1"}, r.args...)
		if err := run(ctx, r.bin, ins...); err != nil {
			return err
		}
	}
	return nil
}
