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
// metadata guard does not cover them) must never reach through the host's
// forwarding path: the Hetzner metadata service and the tailnet (CGNAT range
// and the Tailscale ULA prefix). DOCKER-USER sees forwarded traffic only.
var guardRules = []guardRule{
	{"iptables", []string{"-d", "169.254.169.254/32", "-j", "DROP"}},
	{"iptables", []string{"-d", "100.64.0.0/10", "-j", "DROP"}},
	{"ip6tables", []string{"-d", "fd7a:115c:a1e0::/48", "-j", "DROP"}},
}

// bridgeIfaces are the root daemon's bridge interfaces: the default bridge
// and every user-defined network (each agent run has its own).
var bridgeIfaces = []string{"docker0", "br-+"}

// inputRules returns, for one family and one bridge interface, the INPUT
// rules that keep containers off the host itself (the bridge gateway, the
// box's tailnet address, anything listening on 0.0.0.0): replies to
// connections the host opened (previews: the host reaches a container by its
// IP, the container's answer arrives through INPUT) are accepted, everything
// else a container sends to the host is dropped. Order matters: accept first.
func inputRules(iface string) (accept, drop []string) {
	accept = []string{"-i", iface, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}
	drop = []string{"-i", iface, "-j", "DROP"}
	return
}

// EnsureNetGuard makes sure each DROP rule is in DOCKER-USER, inserting the
// missing ones at the top, and that INPUT drops traffic from Docker bridges
// to the host except replies (iptables and ip6tables). An insert failure is
// returned: callers fail closed.
func EnsureNetGuard(ctx context.Context, run Runner) error {
	for _, r := range guardRules {
		if err := ensureRule(ctx, run, r.bin, "DOCKER-USER", r.args); err != nil {
			return err
		}
	}
	for _, bin := range []string{"iptables", "ip6tables"} {
		for _, iface := range bridgeIfaces {
			accept, drop := inputRules(iface)
			has := func(args []string) bool {
				return run(ctx, bin, append([]string{"-w", "-C", "INPUT"}, args...)...) == nil
			}
			ins := func(args []string) error {
				return run(ctx, bin, append([]string{"-w", "-I", "INPUT", "1"}, args...)...)
			}
			dropAdded := false
			if !has(drop) {
				if err := ins(drop); err != nil {
					return err
				}
				dropAdded = true
			}
			if has(accept) {
				if !dropAdded {
					continue
				}
				// The new DROP went in at the top, above the existing
				// ACCEPT: move the ACCEPT back above it.
				if err := run(ctx, bin, append([]string{"-w", "-D", "INPUT"}, accept...)...); err != nil {
					return err
				}
			}
			if err := ins(accept); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureRule(ctx context.Context, run Runner, bin, chain string, args []string) error {
	if run(ctx, bin, append([]string{"-w", "-C", chain}, args...)...) == nil {
		return nil
	}
	return run(ctx, bin, append([]string{"-w", "-I", chain, "1"}, args...)...)
}
