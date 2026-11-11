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

// fakeTables keeps per-binary chains and implements -N, -F, -A, -C, -I <pos>
// and -D. INPUT and DOCKER-USER always exist; user chains need -N. A jump to
// a missing JAUNTBOX-* chain fails.
type fakeTables struct {
	chains map[string][]string // "<bin> <chain>" -> rules, top first
	calls  [][]string
	failOn func(args []string) bool
}

func (f *fakeTables) exists(key string) bool {
	if _, ok := f.chains[key]; ok {
		return true
	}
	return strings.HasSuffix(key, " INPUT") || strings.HasSuffix(key, " DOCKER-USER")
}

func (f *fakeTables) jumpOK(bin, rule string) bool {
	fields := strings.Fields(rule)
	for i, w := range fields {
		if w == "-j" && i+1 < len(fields) && strings.HasPrefix(fields[i+1], "JAUNTBOX-") {
			return f.exists(bin + " " + fields[i+1])
		}
	}
	return true
}

func (f *fakeTables) run(_ context.Context, bin string, args ...string) error {
	f.calls = append(f.calls, append([]string{bin}, args...))
	if f.chains == nil {
		f.chains = map[string][]string{}
	}
	if len(args) < 3 || args[0] != "-w" {
		return errors.New("bad call")
	}
	if f.failOn != nil && f.failOn(args) {
		return errors.New("iptables broke")
	}
	key := bin + " " + args[2]
	rest := args[3:]
	switch args[1] {
	case "-N":
		if f.exists(key) {
			return errors.New("chain already exists")
		}
		f.chains[key] = []string{}
		return nil
	case "-F":
		if !f.exists(key) {
			return errors.New("no such chain")
		}
		f.chains[key] = []string{}
		return nil
	case "-A":
		if !f.exists(key) || !f.jumpOK(bin, strings.Join(rest, " ")) {
			return errors.New("no such chain")
		}
		f.chains[key] = append(append([]string{}, f.chains[key]...), strings.Join(rest, " "))
		return nil
	case "-C", "-D":
		if !f.exists(key) {
			return errors.New("no such chain")
		}
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
		if err != nil || pos < 1 || pos > len(f.chains[key])+1 || !f.exists(key) || !f.jumpOK(bin, strings.Join(rest[1:], " ")) {
			return errors.New("bad position or chain")
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
	jumpHost      = "-i br-+ -j JAUNTBOX-HOST"
	jumpRuns      = "-i br-+ -j JAUNTBOX-RUNS"
)

func TestNetGuardInsertsMissingRules(t *testing.T) {
	f := &fakeRun{failC: true}
	if err := EnsureNetGuard(context.Background(), f.run, nil); err != nil {
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
	want = append(want,
		[]string{"iptables", "-w", "-N", "JAUNTBOX-HOST"},
		[]string{"iptables", "-w", "-N", "JAUNTBOX-RUNS"},
		[]string{"iptables", "-w", "-F", "JAUNTBOX-HOST"},
	)
	for _, bin := range []string{"iptables", "ip6tables"} {
		for _, iface := range []string{"docker0", "br-+"} {
			acc := []string{"-i", iface, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}
			drop := []string{"-i", iface, "-j", "DROP"}
			rules := [][]string{acc, drop}
			if bin == "iptables" && iface == "br-+" {
				rules = [][]string{acc, {"-i", iface, "-j", "JAUNTBOX-HOST"}, {"-i", iface, "-j", "JAUNTBOX-RUNS"}, drop}
			}
			for i := len(rules) - 1; i >= 0; i-- {
				want = append(want,
					append([]string{bin, "-w", "-C", "INPUT"}, rules[i]...),
					append([]string{bin, "-w", "-I", "INPUT", "1"}, rules[i]...),
				)
			}
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
			pairs := [][2]string{{acceptDocker0, dropDocker0}, {acceptBr, dropBr}}
			if bin == "iptables" {
				pairs = append(pairs, [2]string{acceptBr, jumpHost}, [2]string{jumpHost, jumpRuns}, [2]string{jumpRuns, dropBr})
			}
			for _, p := range pairs {
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
		if err := EnsureNetGuard(context.Background(), f.run, nil); err != nil {
			t.Fatal(err)
		}
		check(t, f)
		want := []string{acceptBr, jumpHost, jumpRuns, dropBr, acceptDocker0, dropDocker0}
		if got := f.chains["iptables INPUT"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("INPUT %q\nwant %q", got, want)
		}
	})
	t.Run("drop missing, accept present", func(t *testing.T) {
		f := &fakeTables{chains: map[string][]string{
			"iptables INPUT":  {acceptDocker0, acceptBr, "-A host rule"},
			"ip6tables INPUT": {acceptBr, dropDocker0},
		}}
		if err := EnsureNetGuard(context.Background(), f.run, nil); err != nil {
			t.Fatal(err)
		}
		check(t, f)
	})
	t.Run("rerun changes nothing", func(t *testing.T) {
		f := &fakeTables{}
		if err := EnsureNetGuard(context.Background(), f.run, nil); err != nil {
			t.Fatal(err)
		}
		before := map[string][]string{}
		for k, v := range f.chains {
			before[k] = append([]string{}, v...)
		}
		f.calls = nil
		if err := EnsureNetGuard(context.Background(), f.run, nil); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.chains, before) {
			t.Fatalf("chains changed:\n%v\n%v", f.chains, before)
		}
		for _, c := range f.calls {
			if c[2] != "-C" && c[2] != "-N" && c[2] != "-F" {
				t.Fatalf("unexpected non-check call %v", c)
			}
		}
	})
}

func TestNetGuardIdempotent(t *testing.T) {
	f := &fakeRun{}
	if err := EnsureNetGuard(context.Background(), f.run, nil); err != nil {
		t.Fatal(err)
	}
	// 3 DOCKER-USER checks, -N -N -F, then the INPUT checks: iptables
	// 2 (docker0) + 4 (br-+), ip6tables 2 + 2
	if len(f.calls) != 3+3+6+4 {
		t.Fatalf("calls = %v", f.calls)
	}
	for _, c := range f.calls {
		if c[2] != "-C" && c[2] != "-N" && c[2] != "-F" {
			t.Fatalf("unexpected non-check call %v", c)
		}
	}
}

func TestNetGuardFailsClosed(t *testing.T) {
	f := &fakeRun{failC: true, failI: true}
	if err := EnsureNetGuard(context.Background(), f.run, nil); err == nil {
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
	if err := EnsureNetGuard(context.Background(), failInput, nil); err == nil {
		t.Fatal("an INPUT -I failure must be returned")
	}
}

func in(t *testing.T, f *fakeTables, key string) []string {
	t.Helper()
	return f.chains[key]
}

func relOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	last := -1
	for _, w := range want {
		found := -1
		for i, g := range got {
			if g == w {
				if found >= 0 {
					t.Fatalf("duplicate %q in %q", w, got)
				}
				found = i
			}
		}
		if found < 0 || found < last {
			t.Fatalf("order wrong for %q in %q", w, got)
		}
		last = found
	}
}

func TestNetGuardHostPortsOrder(t *testing.T) {
	f := &fakeTables{}
	if err := EnsureNetGuard(context.Background(), f.run, []int{6379, 5432}); err != nil {
		t.Fatal(err)
	}
	got := in(t, f, "iptables INPUT")
	if len(got) != 6 {
		t.Fatalf("INPUT %q", got)
	}
	relOrder(t, got, acceptDocker0, dropDocker0)
	relOrder(t, got, acceptBr, jumpHost, jumpRuns, dropBr)
	want := []string{"-p tcp --dport 5432 -j ACCEPT", "-p tcp --dport 6379 -j ACCEPT"}
	if h := f.chains["iptables JAUNTBOX-HOST"]; !reflect.DeepEqual(h, want) {
		t.Fatalf("HOST %q", h)
	}
	if r, ok := f.chains["iptables JAUNTBOX-RUNS"]; !ok || len(r) != 0 {
		t.Fatalf("RUNS %q %v", r, ok)
	}
	if got6 := f.chains["ip6tables INPUT"]; len(got6) != 4 {
		t.Fatalf("ip6tables INPUT %q", got6)
	}
	for k := range f.chains {
		if strings.HasPrefix(k, "ip6tables JAUNTBOX-") {
			t.Fatalf("ip6tables chain %q", k)
		}
	}
}

func TestNetGuardUpgradesPlan03Input(t *testing.T) {
	f := &fakeTables{chains: map[string][]string{
		"iptables INPUT": {acceptBr, dropBr, acceptDocker0, dropDocker0},
	}}
	if err := EnsureNetGuard(context.Background(), f.run, nil); err != nil {
		t.Fatal(err)
	}
	got := f.chains["iptables INPUT"]
	if len(got) != 6 {
		t.Fatalf("INPUT %q", got)
	}
	relOrder(t, got, acceptBr, jumpHost, jumpRuns, dropBr)
}

func TestNetGuardRefillDropsStalePorts(t *testing.T) {
	f := &fakeTables{}
	ctx := context.Background()
	if err := EnsureNetGuard(ctx, f.run, []int{5432, 6379}); err != nil {
		t.Fatal(err)
	}
	pre := "-i br-jbabc -p tcp --dport 80 -j ACCEPT"
	f.chains["iptables JAUNTBOX-RUNS"] = []string{pre}
	if err := EnsureNetGuard(ctx, f.run, []int{5432}); err != nil {
		t.Fatal(err)
	}
	if h := f.chains["iptables JAUNTBOX-HOST"]; !reflect.DeepEqual(h, []string{"-p tcp --dport 5432 -j ACCEPT"}) {
		t.Fatalf("HOST %q", h)
	}
	if r := f.chains["iptables JAUNTBOX-RUNS"]; !reflect.DeepEqual(r, []string{pre}) {
		t.Fatalf("RUNS %q", r)
	}
}

func TestNetGuardReappliesAfterFlush(t *testing.T) {
	f := &fakeTables{}
	ctx := context.Background()
	if err := EnsureNetGuard(ctx, f.run, nil); err != nil {
		t.Fatal(err)
	}
	f.chains["iptables INPUT"] = []string{}
	if err := EnsureNetGuard(ctx, f.run, nil); err != nil {
		t.Fatal(err)
	}
	got := f.chains["iptables INPUT"]
	if len(got) != 6 {
		t.Fatalf("INPUT %q", got)
	}
	relOrder(t, got, acceptBr, jumpHost, jumpRuns, dropBr)
	relOrder(t, got, acceptDocker0, dropDocker0)
	// only dropBr deleted
	var kept []string
	for _, r := range got {
		if r != dropBr {
			kept = append(kept, r)
		}
	}
	f.chains["iptables INPUT"] = kept
	if err := EnsureNetGuard(ctx, f.run, nil); err != nil {
		t.Fatal(err)
	}
	got = f.chains["iptables INPUT"]
	if len(got) != 6 {
		t.Fatalf("INPUT %q", got)
	}
	relOrder(t, got, acceptBr, jumpHost, jumpRuns, dropBr)
}

func TestNetGuardIdempotentWithPorts(t *testing.T) {
	f := &fakeTables{}
	ctx := context.Background()
	if err := EnsureNetGuard(ctx, f.run, []int{5432}); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := EnsureNetGuard(ctx, f.run, []int{5432}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c[3] == "INPUT" && (c[2] == "-I" || c[2] == "-D") {
			t.Fatalf("INPUT changed: %v", c)
		}
	}
	if h := f.chains["iptables JAUNTBOX-HOST"]; len(h) != 1 {
		t.Fatalf("HOST %q", h)
	}
}

func TestNetGuardFailsClosedOnChain(t *testing.T) {
	f := &fakeTables{failOn: func(a []string) bool { return a[1] == "-F" && a[2] == "JAUNTBOX-HOST" }}
	if err := EnsureNetGuard(context.Background(), f.run, nil); err == nil {
		t.Fatal("-F failure must be returned")
	}
	for _, c := range f.calls {
		if c[2] == "-I" && c[3] == "INPUT" {
			t.Fatalf("INPUT touched after chain failure: %v", c)
		}
	}
	g := &fakeTables{failOn: func(a []string) bool { return a[1] == "-A" && a[2] == "JAUNTBOX-HOST" }}
	if err := EnsureNetGuard(context.Background(), g.run, []int{5432}); err == nil {
		t.Fatal("-A failure must be returned")
	}
}

func TestNetGuardRejectsInvalidPorts(t *testing.T) {
	f := &fakeTables{}
	if err := EnsureNetGuard(context.Background(), f.run, []int{0}); err == nil {
		t.Fatal("want validation error")
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestRunPortRulesScopedToBridge(t *testing.T) {
	f := &fakeTables{}
	ctx := context.Background()
	if err := EnsureNetGuard(ctx, f.run, []int{80}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := AllowRunPorts(ctx, f.run, "run-1", []int{5432}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"-i " + BridgeName("run-1") + " -p tcp --dport 5432 -j ACCEPT"}
	if r := f.chains["iptables JAUNTBOX-RUNS"]; !reflect.DeepEqual(r, want) {
		t.Fatalf("RUNS %q", r)
	}
	for _, k := range []string{"iptables JAUNTBOX-RUNS", "iptables JAUNTBOX-HOST"} {
		for _, r := range f.chains[k] {
			if strings.Contains(r, "-d ") {
				t.Fatalf("destination match in %q", r)
			}
		}
	}
}

func TestAllowRunPortsFailsClosed(t *testing.T) {
	f := &fakeTables{}
	ctx := context.Background()
	if err := EnsureNetGuard(ctx, f.run, nil); err != nil {
		t.Fatal(err)
	}
	n := 0
	f.failOn = func(a []string) bool {
		if a[1] == "-A" && a[2] == "JAUNTBOX-RUNS" {
			n++
			return n == 2
		}
		return false
	}
	if err := AllowRunPorts(ctx, f.run, "run-1", []int{5432, 6379}); err == nil {
		t.Fatal("want error")
	}
	if r := f.chains["iptables JAUNTBOX-RUNS"]; len(r) != 0 {
		t.Fatalf("RUNS %q", r)
	}
}

func TestRevokeRunPortsLeavesHostChain(t *testing.T) {
	f := &fakeTables{}
	ctx := context.Background()
	if err := EnsureNetGuard(ctx, f.run, []int{5432}); err != nil {
		t.Fatal(err)
	}
	if err := AllowRunPorts(ctx, f.run, "run-1", []int{6379}); err != nil {
		t.Fatal(err)
	}
	RevokeRunPorts(ctx, f.run, "run-1", []int{6379})
	RevokeRunPorts(ctx, f.run, "run-1", []int{6379})
	if r := f.chains["iptables JAUNTBOX-RUNS"]; len(r) != 0 {
		t.Fatalf("RUNS %q", r)
	}
	if h := f.chains["iptables JAUNTBOX-HOST"]; len(h) != 1 {
		t.Fatalf("HOST %q", h)
	}
}

func TestBridgeName(t *testing.T) {
	a, b := BridgeName("run-1"), BridgeName("run-2")
	if len(a) != 15 || !strings.HasPrefix(a, "br-jb") || !strings.HasPrefix(a, "br-") {
		t.Fatalf("name %q", a)
	}
	if a != BridgeName("run-1") || a == b {
		t.Fatalf("not stable/distinct: %q %q", a, b)
	}
}

func TestAllowRunPortsRollbackSurvivesCancel(t *testing.T) {
	f := &fakeTables{}
	if err := EnsureNetGuard(context.Background(), f.run, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	run := func(c context.Context, bin string, args ...string) error {
		if len(args) > 1 && args[1] == "-A" {
			n++
			if n == 2 {
				cancel()
				return errors.New("interrupted")
			}
		}
		if c.Err() != nil {
			return c.Err()
		}
		return f.run(c, bin, args...)
	}
	if err := AllowRunPorts(ctx, run, "run-1", []int{5432, 6379}); err == nil {
		t.Fatal("want error")
	}
	if r := f.chains["iptables JAUNTBOX-RUNS"]; len(r) != 0 {
		t.Fatalf("rollback left rules: %q", r)
	}
}
