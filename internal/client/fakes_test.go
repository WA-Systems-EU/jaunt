package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

const (
	keyID, fwID, volID = 11, 22, 33
	volDevice          = "/dev/disk/by-id/scsi-0HC_Volume_33"
)

type named struct {
	name   string
	labels map[string]string
}

type fakeProv struct {
	box                  *provider.Box
	calls                []string
	spec                 provider.CreateBoxSpec
	key, fw, vol         named
	volCalls             int
	failCreate           bool
	racedBox             *provider.Box // appears after CreateBox fails
	seq                  *[]string     // optional shared call log across fakes
	deleted              []int64
	waitErr              error // WaitDeleted fails while set
	clearWaitErrOnDelete bool
}

func (f *fakeProv) log(s string) {
	if f.seq != nil {
		*f.seq = append(*f.seq, s)
	}
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
func (f *fakeProv) EnsureSSHKey(_ context.Context, name, _ string, l map[string]string) (int64, error) {
	f.rec("key")
	f.key = named{name, l}
	return keyID, nil
}
func (f *fakeProv) EnsureFirewall(_ context.Context, name string, l map[string]string) (int64, error) {
	f.rec("fw")
	f.fw = named{name, l}
	return fwID, nil
}
func (f *fakeProv) EnsureVolume(_ context.Context, name string, _ int, _ string, l map[string]string) (provider.Volume, error) {
	f.rec("vol")
	f.vol = named{name, l}
	f.volCalls++
	return provider.Volume{ID: volID, Device: volDevice}, nil
}
func (f *fakeProv) FindBox(_ context.Context, l map[string]string) (*provider.Box, error) {
	f.rec("find")
	if !reflect.DeepEqual(l, map[string]string{"jauntbox": "alice", "role": "box"}) {
		return nil, nil
	}
	return f.box, nil
}
func (f *fakeProv) CreateBox(_ context.Context, s provider.CreateBoxSpec) (provider.Box, error) {
	f.rec("create")
	f.log("create:" + s.ServerType)
	f.spec = s
	if f.failCreate {
		f.box = f.racedBox
		return provider.Box{}, errors.New("uniqueness_error")
	}
	b := provider.Box{ID: 9, Name: s.Name}
	f.box = &b
	return b, nil
}
func (f *fakeProv) DeleteBox(_ context.Context, id int64) error {
	f.rec("delete")
	f.log("delete")
	f.deleted = append(f.deleted, id)
	if f.clearWaitErrOnDelete {
		f.waitErr = nil
	}
	return nil
}
func (f *fakeProv) WaitDeleted(context.Context, int64) error {
	f.rec("waitdeleted")
	f.log("waitdeleted")
	if f.waitErr != nil {
		return f.waitErr
	}
	f.box = nil
	return nil
}

type fakeMesh struct {
	keys   int
	tags   []string
	ttl    time.Duration
	falses int // false answers before true; -1 = never online
	errs   int // leading errors before normal answers
	polls  int
}

func (m *fakeMesh) CreateAuthKey(_ context.Context, tags []string, ttl time.Duration) (string, error) {
	m.keys++
	m.tags, m.ttl = tags, ttl
	return "tskey-auth-X", nil
}
func (m *fakeMesh) DeviceOnline(context.Context, string) (bool, error) {
	m.polls++
	if m.polls <= m.errs {
		return false, errors.New("transient")
	}
	if m.falses < 0 {
		return false, nil
	}
	return m.polls-m.errs > m.falses, nil
}

type fakeRemote struct {
	order []string
	cmds  []string
	err   error     // returned by Run
	out   []byte    // returned by Run
	seq   *[]string // optional shared call log
}

func (r *fakeRemote) Run(_ context.Context, host string, args ...string) ([]byte, error) {
	r.order = append(r.order, "run")
	r.cmds = append(r.cmds, fmt.Sprint(args))
	if r.seq != nil {
		*r.seq = append(*r.seq, "run:"+strings.Join(args, " "))
	}
	return r.out, r.err
}
func (r *fakeRemote) Copy(_ context.Context, host, local, remote string) error {
	r.order = append(r.order, "copy:"+remote)
	return nil
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }
