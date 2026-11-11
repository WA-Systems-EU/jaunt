package cloudinit

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/config"
)

type wf struct {
	Path        string `yaml:"path"`
	Permissions string `yaml:"permissions"`
	Encoding    string `yaml:"encoding"`
	Content     string `yaml:"content"`
}

type doc struct {
	WriteFiles []wf     `yaml:"write_files"`
	Runcmd     []string `yaml:"runcmd"`
}

func testParams() Params {
	return Params{
		Box: boxinfo.BoxInfo{
			User: "alice", Hostname: "jauntbox-alice", Profile: "default", Timezone: "Europe/Paris",
			UpAt: time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC),
			Limits: config.Limits{
				MaxUptime: config.Duration(10 * time.Hour), EveningCutoff: "19:00",
				IdleDay: config.Duration(2 * time.Hour), IdleNight: config.Duration(time.Hour),
				NightHours: "22:00-07:00", HumanActiveWindow: config.Duration(30 * time.Minute),
				WrapUpGrace: config.Duration(5 * time.Minute),
			},
		},
		TailscaleAuthKey: "tskey-auth-TEST", HetznerToken: "hc-TEST",
		VolumeDevice: "/dev/disk/by-id/scsi-0HC_Volume_1", Tag: "tag:jauntbox-alice",
	}
}

func render(t *testing.T) (string, doc) {
	t.Helper()
	out, err := Render(testParams())
	if err != nil {
		t.Fatal(err)
	}
	var d doc
	if err := yaml.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("not yaml: %v", err)
	}
	return out, d
}

func content(t *testing.T, d doc, path string) (wf, string) {
	t.Helper()
	for _, f := range d.WriteFiles {
		if f.Path == path {
			c := f.Content
			if f.Encoding == "" { // written inline by the template
				return f, c
			}
			if f.Encoding != "gz+b64" {
				t.Fatalf("%s: encoding %q, want gz+b64", path, f.Encoding)
			}
			b, err := base64.StdEncoding.DecodeString(c)
			if err != nil {
				t.Fatal(err)
			}
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(zr)
			if err != nil {
				t.Fatal(err)
			}
			c = string(raw)
			return f, c
		}
	}
	t.Fatalf("no write_file %s", path)
	return wf{}, ""
}

func TestRenderIsCloudConfig(t *testing.T) {
	out, d := render(t)
	if !strings.HasPrefix(out, "#cloud-config\n") {
		t.Fatal("missing #cloud-config header")
	}
	if len(d.WriteFiles) == 0 || len(d.Runcmd) == 0 {
		t.Fatal("write_files and runcmd required")
	}
}

func TestRenderSecretsOnlyInFiles(t *testing.T) {
	out, d := render(t)
	for _, c := range d.Runcmd {
		if strings.Contains(c, "tskey-auth-TEST") || strings.Contains(c, "hc-TEST") {
			t.Fatalf("secret in runcmd: %s", c)
		}
	}
	_, ts := content(t, d, "/etc/jauntbox/ts.key")
	_, hc := content(t, d, "/etc/jauntbox/hcloud.token")
	if !strings.Contains(ts, "tskey-auth-TEST") || !strings.Contains(hc, "hc-TEST") {
		t.Fatal("secrets missing from files")
	}
	for _, f := range d.WriteFiles {
		if f.Path == "/etc/jauntbox/ts.key" || f.Path == "/etc/jauntbox/hcloud.token" {
			if f.Permissions != "0600" {
				t.Fatalf("%s perms %s", f.Path, f.Permissions)
			}
			continue
		}
		_, c := content(t, d, f.Path)
		if strings.Contains(c, "tskey-auth-TEST") || strings.Contains(c, "hc-TEST") {
			t.Fatalf("secret leaked into %s", f.Path)
		}
	}
	_ = out
}

func TestRenderBoxInfoRoundTrip(t *testing.T) {
	_, d := render(t)
	_, c := content(t, d, boxinfo.Path)
	var got boxinfo.BoxInfo
	if err := json.Unmarshal([]byte(c), &got); err != nil {
		t.Fatal(err)
	}
	want := testParams().Box
	if !got.UpAt.Equal(want.UpAt) {
		t.Fatalf("UpAt %v != %v", got.UpAt, want.UpAt)
	}
	got.UpAt = want.UpAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestRenderUsesTagAndHostname(t *testing.T) {
	_, d := render(t)
	all := strings.Join(d.Runcmd, "\n")
	for _, s := range []string{"--hostname=jauntbox-alice", "--advertise-tags=tag:jauntbox-alice"} {
		if !strings.Contains(all, s) {
			t.Fatalf("runcmd missing %s", s)
		}
	}
}

func TestRenderEnablesOnlySafetyTimer(t *testing.T) {
	_, d := render(t)
	all := strings.Join(d.Runcmd, "\n")
	if !strings.Contains(all, "systemctl enable --now jauntbox-safety.timer") {
		t.Fatal("safety timer not enabled")
	}
	if len(d.Runcmd) < 2 || d.Runcmd[0] != "systemctl daemon-reload" || d.Runcmd[1] != "systemctl enable --now jauntbox-safety.timer" {
		t.Fatalf("safety timer must be armed by runcmd[0..1], got %q", d.Runcmd[:2])
	}
	if strings.Contains(all, "jauntbox-supervise.timer") {
		t.Fatal("supervise timer must not be enabled")
	}
}

func TestRenderWritesUnitsAndScripts(t *testing.T) {
	_, d := render(t)
	for _, p := range []string{
		"/etc/systemd/system/jauntbox-supervise.service", "/etc/systemd/system/jauntbox-supervise.timer",
		"/etc/systemd/system/jauntbox-safety.service", "/etc/systemd/system/jauntbox-safety.timer",
		"/opt/jauntbox/safety-net.sh", "/opt/jaunt/dev-setup.sh", "/opt/jaunt/dev-setup-user.sh",
		"/opt/jaunt/jaunt-repos.sh", "/etc/jaunt/provision.env",
	} {
		content(t, d, p)
	}
	_, env := content(t, d, "/etc/jaunt/provision.env")
	for _, s := range []string{"JAUNT_USER=alice", "JAUNT_UID=2000", "JAUNT_INSTALL_TOOLCHAIN=true", "JAUNT_DOCKER_DATA_ROOT=/home/.docker"} {
		if !strings.Contains(env, s) {
			t.Fatalf("provision.env missing %s", s)
		}
	}
}

func TestRenderSuperviseServiceHasStartTimeout(t *testing.T) {
	_, d := render(t)
	_, unit := content(t, d, "/etc/systemd/system/jauntbox-supervise.service")
	if !strings.Contains(unit, "TimeoutStartSec=90") {
		t.Fatalf("supervise.service lacks TimeoutStartSec=90:\n%s", unit)
	}
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/jauntctl supervise\n") {
		t.Fatalf("supervise.service must run jauntctl supervise:\n%s", unit)
	}
}

func TestRenderPinsTailscaledStateViaDefaultsFile(t *testing.T) {
	_, d := render(t)
	idx := func(sub string) int {
		for i, c := range d.Runcmd {
			if strings.Contains(c, sub) {
				return i
			}
		}
		return -1
	}
	install, flags, restart := idx("tailscale.com/install.sh"), idx("/etc/default/tailscaled"), idx("systemctl restart tailscaled")
	if install < 0 || flags < 0 || restart < 0 || !(install < flags && flags < restart) {
		t.Fatalf("want install(%d) < FLAGS write(%d) < restart(%d)", install, flags, restart)
	}
	for _, s := range []string{`FLAGS="--state=/home/.jaunt/tailscaled.state"`, "s|^FLAGS=.*|"} {
		if !strings.Contains(d.Runcmd[flags], s) {
			t.Errorf("FLAGS command lacks %q: %s", s, d.Runcmd[flags])
		}
	}
	_, dropin := content(t, d, "/etc/systemd/system/tailscaled.service.d/10-jaunt-state.conf")
	if strings.Contains(dropin, "Environment=FLAGS") {
		t.Errorf("redundant Environment=FLAGS still in drop-in:\n%s", dropin)
	}
}

func TestRenderInstallsMetadataGuard(t *testing.T) {
	_, d := render(t)
	_, unit := content(t, d, "/etc/systemd/system/jauntbox-metadata-guard.service")
	want := `ExecStart=/bin/sh -c 'iptables -C OUTPUT -d 169.254.169.254/32 -m owner ! --uid-owner 0 -j DROP 2>/dev/null || iptables -I OUTPUT 1 -d 169.254.169.254/32 -m owner ! --uid-owner 0 -j DROP'`
	if !strings.Contains(unit, want) {
		t.Fatalf("unit missing exact ExecStart:\n%s", unit)
	}
	enable, up := -1, -1
	for i, c := range d.Runcmd {
		if c == "systemctl enable --now jauntbox-metadata-guard.service" {
			enable = i
		}
		if strings.HasPrefix(c, "tailscale up") {
			up = i
		}
	}
	if enable < 0 || up < 0 || enable > up {
		t.Fatalf("guard enable (%d) must precede tailscale up (%d)", enable, up)
	}
}

// Hetzner refuses user_data over 32 KiB ("invalid input in field 'user_data'").
// Keep headroom for longer secrets and box info.
func TestRenderFitsHetznerUserDataLimit(t *testing.T) {
	out, _ := render(t)
	if n := len(out); n > 28*1024 {
		t.Fatalf("user-data is %d bytes; Hetzner's limit is 32768 and we keep 4 KiB headroom", n)
	}
}
