package client

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

func TestSSHKeysOrder(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	must(t, os.MkdirAll(ssh, 0o700))
	for _, n := range []string{"id_rsa.pub", "a_work.pub", "id_ed25519.pub", "id_rsa", "known_hosts"} {
		must(t, os.WriteFile(filepath.Join(ssh, n), []byte("ssh-x K\n"), 0o644))
	}
	got := SSHKeys(home)
	want := []string{filepath.Join(ssh, "id_ed25519.pub"), filepath.Join(ssh, "a_work.pub"), filepath.Join(ssh, "id_rsa.pub")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if len(SSHKeys(t.TempDir())) != 0 {
		t.Error("no keys expected")
	}
}

func TestFormLocationDefaultFromVolume(t *testing.T) {
	if got := defaultLocation(true, "nbg1"); got != "nbg1" {
		t.Errorf("found: %q", got)
	}
	if got := defaultLocation(false, ""); got != "fsn1" {
		t.Errorf("none: %q", got)
	}
	if got := defaultLocation(true, ""); got != "fsn1" {
		t.Errorf("found without location: %q", got)
	}
	if n := volumeNote(true, "nbg1", "fsn1"); n != "your home volume is in nbg1; volumes can't move, so the next up would fail" {
		t.Errorf("note %q", n)
	}
	if volumeNote(true, "nbg1", "nbg1") != "" || volumeNote(false, "", "fsn1") != "" {
		t.Error("no note expected")
	}
}

func TestFormTypeOptions(t *testing.T) {
	cases := []struct {
		t    provider.ServerType
		want string
	}{
		{provider.ServerType{Name: "cpx42", Cores: 8, MemoryGB: 16, PriceMonthly: map[string]float64{"fsn1": 12.49}}, "cpx42  8 vCPU · 16 GB  shared  €12.49/mo"},
		{provider.ServerType{Name: "ccx33", Cores: 8, MemoryGB: 32, PriceMonthly: map[string]float64{"fsn1": 60}}, "ccx33  8 vCPU · 32 GB  dedicated  €60.00/mo"},
		{provider.ServerType{Name: "cx22", Cores: 2, MemoryGB: 4, PriceMonthly: map[string]float64{"fsn1": 4}}, "cx22  2 vCPU · 4 GB  shared  €4.00/mo"},
	}
	for _, c := range cases {
		if got := typeOptionLabel(c.t, "fsn1"); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
	opts := typeOptionsFor(fixtureTypes(), "fsn1")
	var names []string
	for _, o := range opts {
		names = append(names, o.Name)
	}
	if strings.Join(names, ",") != "cx22,cpx42,ccx23,ccx33,ccx43" {
		t.Errorf("options %v (arm, deprecated and unpriced types must be left out; sorted by memory then price)", names)
	}
	locs := typeLocations(fixtureTypes())
	if !reflect.DeepEqual(locs, []string{"fsn1", "nbg1"}) {
		t.Errorf("locations %v", locs)
	}
}
