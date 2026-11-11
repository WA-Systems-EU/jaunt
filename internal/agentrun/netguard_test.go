package agentrun

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type fakeRun struct {
	calls [][]string
	failC bool
	failI bool
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) error {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	joined := strings.Join(args, " ")
	if f.failC && strings.HasPrefix(joined, "-w -C ") {
		return errors.New("no such rule")
	}
	if f.failI && strings.HasPrefix(joined, "-w -I ") {
		return errors.New("iptables broke")
	}
	return nil
}

// fakeTables keeps per-binary chains and implements -C, -I <pos> and -D.
type fakeTables struct {
	chains map[string][]string // "<bin> <chain>" -> rules, top first
	calls  [][]string
}

func (f *fakeTables) run(_ context.Context, bin string, args ...string) error {
	f.calls = append(f.calls, append([]string{bin}, args...))
	if f.chains == nil {
		f.chains = map[string][]string{}
	}
	if len(args) < 3 || args[0] != "-w" {
		return errors.New("bad call")
	}
	key := bin + " " + args[2]
	rest := args[3:]
	switch args[1] {
	case "-C", "-D":
		rule := strings.Join(rest, " ")
		for i, r := range f.chains[key] {
			if r == rule {
				if args[1] == "-D" {
					f.chains[key] = append(append([]string{}, f.chains[key][:i]...), f.chains[key][i+1:]...)
				}
				return nil
			}
		}
		return errors.New("no such rule")
	case "-I":
		pos, err := strconv.Atoi(rest[0])
		if err != nil || pos < 1 || pos > len(f.chains[key])+1 {
			return errors.New("bad position")
		}
		rules := append([]string{}, f.chains[key][:pos-1]...)
		rules = append(rules, strings.Join(rest[1:], " "))
		f.chains[key] = append(rules, f.chains[key][pos-1:]...)
		return nil
	}
	return errors.New("bad op")
}

const (
	acceptDocker0 = "-i docker0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
	dropDocker0   = "-i docker0 -j DROP"
	acceptBr      = "-i br-+ -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
	dropBr        = "-i br-+ -j DROP"
)

func TestNetGuardInsertsMissingRules(t *testing.T) {
	f := &fakeRun{failC: true}
	if err := EnsureNetGuard(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"iptables", "-w", "-C", "DOCKER-USER", "-d", "169.254.169.254/32", "-j", "DROP"},
		{"iptables", "-w", "-I", "DOCKER-USER", "1", "-d", "169.254.169.254/32", "-j", "DROP"},
		{"iptables", "-w", "-C", "DOCKER-USER", "-d", "100.64.0.0/10", "-j", "DROP"},
		{"iptables", "-w", "-I", "DOCKER-USER", "1", "-d", "100.64.0.0/10", "-j", "DROP"},
		{"ip6tables", "-w", "-C", "DOCKER-USER", "-d", "fd7a:115c:a1e0::/48", "-j", "DROP"},
		{"ip6tables", "-w", "-I", "DOCKER-USER", "1", "-d", "fd7a:115c:a1e0::/48", "-j", "DROP"},
	}
	for _, bin := range []string{"iptables", "ip6tables"} {
		for _, iface := range []string{"docker0", "br-+"} {
			acc := []string{"-i", iface, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}
			drop := []string{"-i", iface, "-j", "DROP"}
			want = append(want,
				append([]string{bin, "-w", "-C", "INPUT"}, drop...),
				append([]string{bin, "-w", "-I", "INPUT", "1"}, drop...),
				append([]string{bin, "-w", "-C", "INPUT"}, acc...),
				append([]string{bin, "-w", "-I", "INPUT", "1"}, acc...),
			)
		}
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %v\nwant %v", f.calls, want)
	}
}

// TestNetGuardInputOrder checks the resulting chains: each ACCEPT for
// replies sits above its interface's DROP, from empty chains, when only the
// DROP was missing (the ACCEPT is moved back above it), and on a re-run.
func TestNetGuardInputOrder(t *testing.T) {
	check := func(t *testing.T, f *fakeTables) {
		t.Helper()
		for _, bin := range []string{"iptables", "ip6tables"} {
			in := f.chains[bin+" INPUT"]
			idx := map[string]int{}
			for i, r := range in {
				if _, dup := idx[r]; dup {
					t.Fatalf("%s INPUT duplicate %q: %q", bin, r, in)
				}
				idx[r] = i
			}
			for _, p := range [][2]string{{acceptDocker0, dropDocker0}, {acceptBr, dropBr}} {
				a, okA := idx[p[0]]
				d, okD := idx[p[1]]
				if !okA || !okD || a > d {
					t.Fatalf("%s INPUT order wrong: %q", bin, in)
				}
			}
			if _, ok := idx["-A host rule"]; ok && idx["-A host rule"] < idx[dropBr] {
				t.Fatalf("%s guard rules must be above the existing rules: %q", bin, in)
			}
		}
		if du := f.chains["iptables DOCKER-USER"]; len(du) != 2 {
			t.Fatalf("DOCKER-USER %q", du)
		}
	}
	t.Run("empty", func(t *testing.T) {
		f := &fakeTables{}
		if err := EnsureNetGuard(context.Background(), f.run); err != nil {
			t.Fatal(err)
		}
		check(t, f)
		want := []string{acceptBr, dropBr, acceptDocker0, dropDocker0}
		if got := f.chains["iptables INPUT"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("INPUT %q\nwant %q", got, want)
		}
	})
	t.Run("drop missing, accept present", func(t *testing.T) {
		f := &fakeTables{chains: map[string][]string{
			"iptables INPUT":  {acceptDocker0, acceptBr, "-A host rule"},
			"ip6tables INPUT": {acceptBr, dropDocker0},
		}}
		if err := EnsureNetGuard(context.Background(), f.run); err != nil {
			t.Fatal(err)
		}
		check(t, f)
	})
	t.Run("rerun changes nothing", func(t *testing.T) {
		f := &fakeTables{}
		if err := EnsureNetGuard(context.Background(), f.run); err != nil {
			t.Fatal(err)
		}
		before := map[string][]string{}
		for k, v := range f.chains {
			before[k] = append([]string{}, v...)
		}
		f.calls = nil
		if err := EnsureNetGuard(context.Background(), f.run); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.chains, before) {
			t.Fatalf("chains changed:\n%v\n%v", f.chains, before)
		}
		for _, c := range f.calls {
			if c[2] != "-C" {
				t.Fatalf("unexpected non-check call %v", c)
			}
		}
	})
}

func TestNetGuardIdempotent(t *testing.T) {
	f := &fakeRun{}
	if err := EnsureNetGuard(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	// 3 DOCKER-USER checks, then 2 families x 2 interfaces x (drop, accept)
	if len(f.calls) != 3+8 {
		t.Fatalf("calls = %v", f.calls)
	}
	for _, c := range f.calls {
		if c[2] != "-C" {
			t.Fatalf("unexpected non-check call %v", c)
		}
	}
}

func TestNetGuardFailsClosed(t *testing.T) {
	f := &fakeRun{failC: true, failI: true}
	if err := EnsureNetGuard(context.Background(), f.run); err == nil {
		t.Fatal("an -I failure must be returned")
	}
	// an INPUT insert failure is returned too
	g := &fakeTables{}
	failInput := func(ctx context.Context, bin string, args ...string) error {
		if len(args) > 2 && args[1] == "-I" && args[2] == "INPUT" {
			return errors.New("iptables broke")
		}
		return g.run(ctx, bin, args...)
	}
	if err := EnsureNetGuard(context.Background(), failInput); err == nil {
		t.Fatal("an INPUT -I failure must be returned")
	}
}
