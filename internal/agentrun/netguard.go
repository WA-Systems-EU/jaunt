// Package agentrun runs agent containers on the box (root side).
package agentrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

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

// Chains holding the ports agents may reach on the host (iptables only).
// hostChain is flushed and refilled by EnsureNetGuard from the box-wide
// ports; runsChain holds per-run rules (-i <bridge>) and is never flushed.
const (
	hostChain = "JAUNTBOX-HOST"
	runsChain = "JAUNTBOX-RUNS"
)

// BridgeName is the bridge interface name of run id's network: br-jb plus
// the first 10 hex of sha256(id) (15 characters, the Linux limit; still
// matched by br-+, and not a name Docker generates).
func BridgeName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "br-jb" + hex.EncodeToString(sum[:])[:10]
}

func sortedPorts(ports []int) []int {
	p := append([]int{}, ports...)
	sort.Ints(p)
	return p
}

func runRule(id string, port int) []string {
	return []string{"-i", BridgeName(id), "-p", "tcp", "--dport", strconv.Itoa(port), "-j", "ACCEPT"}
}

// AllowRunPorts opens ports to run id's bridge (rules in JAUNTBOX-RUNS,
// added only when missing). On a failure it removes the rules this call
// added and returns the error: callers fail closed.
func AllowRunPorts(ctx context.Context, run Runner, id string, ports []int) error {
	var added [][]string
	for _, port := range sortedPorts(ports) {
		rule := runRule(id, port)
		if run(ctx, "iptables", append([]string{"-w", "-C", runsChain}, rule...)...) == nil {
			continue
		}
		if err := run(ctx, "iptables", append([]string{"-w", "-A", runsChain}, rule...)...); err != nil {
			// The rollback must run even when ctx is the reason for the failure.
			rb := context.WithoutCancel(ctx)
			for _, r := range added {
				_ = run(rb, "iptables", append([]string{"-w", "-D", runsChain}, r...)...)
			}
			return err
		}
		added = append(added, rule)
	}
	return nil
}

// RevokeRunPorts removes run id's rules; errors are ignored (the rule names
// a bridge no other run can get, so a leftover one is inert).
func RevokeRunPorts(ctx context.Context, run Runner, id string, ports []int) {
	for _, port := range sortedPorts(ports) {
		_ = run(ctx, "iptables", append([]string{"-w", "-D", runsChain}, runRule(id, port)...)...)
	}
}

// EnsureNetGuard makes sure each DROP rule is in DOCKER-USER, inserting the
// missing ones at the top, and that INPUT drops traffic from Docker bridges
// to the host except replies (iptables and ip6tables). For iptables, br-+
// traffic first passes JAUNTBOX-HOST (refilled here with hostPorts) and
// JAUNTBOX-RUNS (per-run rules, untouched). An insert or chain failure is
// returned: callers fail closed.
func EnsureNetGuard(ctx context.Context, run Runner, hostPorts []int) error {
	if err := config.CheckHostPorts("agent.host_ports", hostPorts); err != nil {
		return err
	}
	for _, r := range guardRules {
		if err := ensureRule(ctx, run, r.bin, "DOCKER-USER", r.args); err != nil {
			return err
		}
	}
	// The chains may exist already: -N errors are ignored; -F then fails
	// only when the chain neither existed nor could be made.
	_ = run(ctx, "iptables", "-w", "-N", hostChain)
	_ = run(ctx, "iptables", "-w", "-N", runsChain)
	if err := run(ctx, "iptables", "-w", "-F", hostChain); err != nil {
		return err
	}
	for _, port := range sortedPorts(hostPorts) {
		if err := run(ctx, "iptables", "-w", "-A", hostChain, "-p", "tcp", "--dport", strconv.Itoa(port), "-j", "ACCEPT"); err != nil {
			return err
		}
	}
	for _, bin := range []string{"iptables", "ip6tables"} {
		for _, iface := range bridgeIfaces {
			accept, drop := inputRules(iface)
			rules := [][]string{accept, drop}
			if bin == "iptables" && iface == "br-+" {
				rules = [][]string{accept,
					{"-i", iface, "-j", hostChain},
					{"-i", iface, "-j", runsChain},
					drop}
			}
			if err := ensureOrdered(ctx, run, bin, "INPUT", rules); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureOrdered makes rules appear in chain in the given order, at the top
// (anything else in the chain stays below). It walks from the last rule to
// the first: a rule is inserted at position 1 when missing, or deleted and
// inserted again when a rule below it was inserted in this pass.
func ensureOrdered(ctx context.Context, run Runner, bin, chain string, rules [][]string) error {
	moved := false
	for i := len(rules) - 1; i >= 0; i-- {
		rule := rules[i]
		present := run(ctx, bin, append([]string{"-w", "-C", chain}, rule...)...) == nil
		if present && !moved {
			continue
		}
		if present {
			if err := run(ctx, bin, append([]string{"-w", "-D", chain}, rule...)...); err != nil {
				return err
			}
		}
		if err := run(ctx, bin, append([]string{"-w", "-I", chain, "1"}, rule...)...); err != nil {
			return err
		}
		moved = true
	}
	return nil
}

func ensureRule(ctx context.Context, run Runner, bin, chain string, args []string) error {
	if run(ctx, bin, append([]string{"-w", "-C", chain}, args...)...) == nil {
		return nil
	}
	return run(ctx, bin, append([]string{"-w", "-I", chain, "1"}, args...)...)
}
