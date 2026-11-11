package agentrun

import (
	"context"
	"errors"
	"reflect"
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
	if f.failC && strings.HasPrefix(joined, "-C ") {
		return errors.New("no such rule")
	}
	if f.failI && strings.HasPrefix(joined, "-I ") {
		return errors.New("iptables broke")
	}
	return nil
}

func TestNetGuardInsertsMissingRules(t *testing.T) {
	f := &fakeRun{failC: true}
	if err := EnsureNetGuard(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"iptables", "-C", "DOCKER-USER", "-d", "169.254.169.254/32", "-j", "DROP"},
		{"iptables", "-I", "DOCKER-USER", "1", "-d", "169.254.169.254/32", "-j", "DROP"},
		{"iptables", "-C", "DOCKER-USER", "-d", "100.64.0.0/10", "-j", "DROP"},
		{"iptables", "-I", "DOCKER-USER", "1", "-d", "100.64.0.0/10", "-j", "DROP"},
		{"ip6tables", "-C", "DOCKER-USER", "-d", "fd7a:115c:a1e0::/48", "-j", "DROP"},
		{"ip6tables", "-I", "DOCKER-USER", "1", "-d", "fd7a:115c:a1e0::/48", "-j", "DROP"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %v\nwant %v", f.calls, want)
	}
}

func TestNetGuardIdempotent(t *testing.T) {
	f := &fakeRun{}
	if err := EnsureNetGuard(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("calls = %v", f.calls)
	}
	for _, c := range f.calls {
		if c[1] != "-C" {
			t.Fatalf("unexpected non-check call %v", c)
		}
	}
}

func TestNetGuardFailsClosed(t *testing.T) {
	f := &fakeRun{failC: true, failI: true}
	if err := EnsureNetGuard(context.Background(), f.run); err == nil {
		t.Fatal("an -I failure must be returned")
	}
}
