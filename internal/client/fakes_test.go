package client

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

type fakeProv struct {
	box   *provider.Box
	calls []string
	spec  provider.CreateBoxSpec
}

func (f *fakeProv) rec(s string) { f.calls = append(f.calls, s) }
func (f *fakeProv) count(s string) int {
	n := 0
	for _, c := range f.calls {
		if c == s {
			n++
		}
	}
	return n
}
func (f *fakeProv) EnsureSSHKey(context.Context, string, string, map[string]string) (int64, error) {
	f.rec("key")
	return 1, nil
}
func (f *fakeProv) EnsureFirewall(context.Context, string, map[string]string) (int64, error) {
	f.rec("fw")
	return 2, nil
}
func (f *fakeProv) EnsureVolume(context.Context, string, int, string, map[string]string) (provider.Volume, error) {
	f.rec("vol")
	return provider.Volume{ID: 3, Device: "/dev/disk/by-id/scsi-0HC_Volume_3"}, nil
}
func (f *fakeProv) FindBox(context.Context, map[string]string) (*provider.Box, error) {
	f.rec("find")
	return f.box, nil
}
func (f *fakeProv) CreateBox(_ context.Context, s provider.CreateBoxSpec) (provider.Box, error) {
	f.rec("create")
	f.spec = s
	b := provider.Box{ID: 9, Name: s.Name}
	f.box = &b
	return b, nil
}
func (f *fakeProv) DeleteBox(context.Context, int64) error   { return nil }
func (f *fakeProv) WaitDeleted(context.Context, int64) error { return nil }

type fakeMesh struct {
	keys   int
	falses int // number of false answers before true; -1 = never online
	polls  int
	order  *[]string
}

func (m *fakeMesh) CreateAuthKey(context.Context, []string, time.Duration) (string, error) {
	m.keys++
	return "tskey-auth-X", nil
}
func (m *fakeMesh) DeviceOnline(context.Context, string) (bool, error) {
	m.polls++
	if m.falses < 0 {
		return false, nil
	}
	return m.polls > m.falses, nil
}

type fakeRemote struct {
	order []string
	cmds  []string
}

func (r *fakeRemote) Run(_ context.Context, host string, args ...string) ([]byte, error) {
	r.order = append(r.order, "run")
	r.cmds = append(r.cmds, fmt.Sprint(args))
	return nil, nil
}
func (r *fakeRemote) Copy(_ context.Context, host, local, remote string) error {
	r.order = append(r.order, "copy:"+remote)
	return nil
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }
