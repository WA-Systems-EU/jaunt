package hetzner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

type rec struct {
	method, path, query string
	body                map[string]any
}

type fake struct {
	mu   sync.Mutex
	reqs []rec
	h    func(r rec, w http.ResponseWriter)
}

func newFake(t *testing.T, h func(r rec, w http.ResponseWriter)) (*Hetzner, *fake) {
	t.Helper()
	f := &fake{h: h}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rc := rec{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		if len(b) > 0 {
			_ = json.Unmarshal(b, &rc.body)
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, rc)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		f.h(rc, w)
	}))
	t.Cleanup(srv.Close)
	return New("tok", hcloud.WithEndpoint(srv.URL), hcloud.WithPollOpts(hcloud.PollOpts{})), f
}

func (f *fake) posts(path string) []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rec
	for _, r := range f.reqs {
		if r.method == "POST" && r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func w(rw http.ResponseWriter, s string) { _, _ = io.WriteString(rw, s) }

const actionsDone = `{"actions":[{"id":1,"status":"success","progress":100}],"meta":{"pagination":{"page":1,"per_page":50,"last_page":1,"total_entries":1}}}`

func (f *fake) gets(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.method == "GET" && r.path == path {
			n++
		}
	}
	return n
}

const volJSON = `{"id":42,"name":"jauntbox-alice-home","size":50,"location":{"name":"%s"},"labels":{}}`

func TestEnsureVolumeIsIdempotent(t *testing.T) {
	exists := false
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) {
		switch {
		case r.method == "GET" && r.path == "/volumes":
			if exists {
				w(rw, `{"volumes":[`+strings.Replace(volJSON, "%s", "fsn1", 1)+`]}`)
			} else {
				w(rw, `{"volumes":[]}`)
			}
		case r.method == "POST" && r.path == "/volumes":
			exists = true
			w(rw, `{"volume":`+strings.Replace(volJSON, "%s", "fsn1", 1)+`,"action":{"id":1,"status":"running"},"next_actions":[]}`)
		case r.method == "GET" && r.path == "/actions":
			w(rw, actionsDone)
		default:
			rw.WriteHeader(404)
		}
	})
	ctx := context.Background()
	labels := map[string]string{"jauntbox": "alice", "role": "home"}
	v, err := h.EnsureVolume(ctx, "jauntbox-alice-home", 50, "fsn1", labels)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != 42 || v.Device != "/dev/disk/by-id/scsi-0HC_Volume_42" {
		t.Fatalf("volume = %+v", v)
	}
	p := f.posts("/volumes")
	if len(p) != 1 {
		t.Fatalf("posts = %d", len(p))
	}
	b := p[0].body
	if b["name"] != "jauntbox-alice-home" || b["size"] != float64(50) || b["location"] != "fsn1" || b["format"] != "ext4" {
		t.Fatalf("body = %v", b)
	}
	if l := b["labels"].(map[string]any); l["jauntbox"] != "alice" || l["role"] != "home" {
		t.Fatalf("labels = %v", l)
	}
	if _, err := h.EnsureVolume(ctx, "jauntbox-alice-home", 50, "fsn1", labels); err != nil {
		t.Fatal(err)
	}
	if n := len(f.posts("/volumes")); n != 1 {
		t.Fatalf("second call posted; total posts = %d", n)
	}
	if f.gets("/actions") == 0 {
		t.Fatal("EnsureVolume did not wait for the create action")
	}
}

func TestEnsureVolumeLocationMismatch(t *testing.T) {
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) {
		w(rw, `{"volumes":[`+strings.Replace(volJSON, "%s", "nbg1", 1)+`]}`)
	})
	_, err := h.EnsureVolume(context.Background(), "jauntbox-alice-home", 50, "fsn1", nil)
	if err == nil || !strings.Contains(err.Error(), "nbg1") || !strings.Contains(err.Error(), "fsn1") {
		t.Fatalf("err = %v", err)
	}
	if len(f.posts("/volumes")) != 0 {
		t.Fatal("created a second volume")
	}
}

func TestFindBoxBySelector(t *testing.T) {
	n := 1
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) {
		s := `{"id":7,"name":"jauntbox-alice","status":"running","created":"2026-11-09T10:00:00+00:00","server_type":{"name":"cpx31"}}`
		if n == 2 {
			s += "," + strings.Replace(s, `"id":7`, `"id":8`, 1)
		}
		w(rw, `{"servers":[`+s+`]}`)
	})
	labels := map[string]string{"role": "box", "jauntbox": "alice"}
	b, err := h.FindBox(context.Background(), labels)
	if err != nil || b == nil || b.ID != 7 || b.ServerType != "cpx31" || b.Status != "running" {
		t.Fatalf("box = %+v err = %v", b, err)
	}
	if q := f.reqs[0].query; !strings.Contains(q, "label_selector=jauntbox%3Dalice%2Crole%3Dbox") {
		t.Fatalf("query = %s", q)
	}
	n = 2
	if _, err := h.FindBox(context.Background(), labels); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("err = %v", err)
	}
}

func TestFindBoxNone(t *testing.T) {
	h, _ := newFake(t, func(r rec, rw http.ResponseWriter) { w(rw, `{"servers":[]}`) })
	b, err := h.FindBox(context.Background(), map[string]string{"jauntbox": "alice", "role": "box"})
	if b != nil || err != nil {
		t.Fatalf("b=%v err=%v", b, err)
	}
}

func TestCreateBoxBody(t *testing.T) {
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) {
		w(rw, `{"server":{"id":9,"name":"jauntbox-alice","status":"initializing","server_type":{"name":"cpx31"}},"action":null,"next_actions":[]}`)
	})
	box, err := h.CreateBox(context.Background(), provider.CreateBoxSpec{
		Name: "jauntbox-alice", ServerType: "cpx31", Image: "ubuntu-24.04", Location: "fsn1",
		UserData: "#cloud-config\n", Labels: map[string]string{"jauntbox": "alice", "role": "box"},
		VolumeID: 42, SSHKeyID: 5, FirewallID: 6,
	})
	if err != nil || box.ID != 9 {
		t.Fatalf("box=%+v err=%v", box, err)
	}
	p := f.posts("/servers")
	if len(p) != 1 {
		t.Fatalf("posts = %d", len(p))
	}
	b := p[0].body
	for k, want := range map[string]any{
		"name": "jauntbox-alice", "server_type": "cpx31", "image": "ubuntu-24.04", "location": "fsn1",
		"user_data": "#cloud-config\n", "automount": false, "start_after_create": true,
	} {
		if b[k] != want {
			t.Errorf("%s = %v, want %v", k, b[k], want)
		}
	}
	if v := b["volumes"].([]any); len(v) != 1 || v[0] != float64(42) {
		t.Errorf("volumes = %v", v)
	}
	if v := b["ssh_keys"].([]any); len(v) != 1 || v[0] != float64(5) {
		t.Errorf("ssh_keys = %v", v)
	}
	if v := b["firewalls"].([]any); len(v) != 1 || v[0].(map[string]any)["firewall"] != float64(6) {
		t.Errorf("firewalls = %v", v)
	}
	if l := b["labels"].(map[string]any); l["jauntbox"] != "alice" || l["role"] != "box" {
		t.Errorf("labels = %v", l)
	}
	pn := b["public_net"].(map[string]any)
	if pn["enable_ipv4"] != true || pn["enable_ipv6"] != true {
		t.Errorf("public_net = %v", pn)
	}
}

func TestEnsureFirewallDeniesInbound(t *testing.T) {
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) {
		if r.path == "/actions" {
			w(rw, actionsDone)
			return
		}
		if r.method == "GET" {
			w(rw, `{"firewalls":[]}`)
			return
		}
		w(rw, `{"firewall":{"id":6,"name":"jauntbox-alice"},"actions":[{"id":1,"status":"running"}]}`)
	})
	id, err := h.EnsureFirewall(context.Background(), "jauntbox-alice", map[string]string{"jauntbox": "alice", "role": "fw"})
	if err != nil || id != 6 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if f.gets("/actions") == 0 {
		t.Fatal("EnsureFirewall did not wait for the create actions")
	}
	b := f.posts("/firewalls")[0].body
	if rules, ok := b["rules"]; ok && len(rules.([]any)) != 0 {
		t.Fatalf("rules = %v", rules)
	}
	at := b["apply_to"].([]any)
	if len(at) != 1 {
		t.Fatalf("apply_to = %v", at)
	}
	a := at[0].(map[string]any)
	if a["type"] != "label_selector" || a["label_selector"].(map[string]any)["selector"] != "jauntbox=alice,role=box" {
		t.Fatalf("apply_to = %v", a)
	}
	if b["labels"].(map[string]any)["role"] != "fw" {
		t.Fatalf("labels = %v", b["labels"])
	}
}

func TestEnsureSSHKey(t *testing.T) {
	exists := false
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) {
		if r.method == "GET" {
			if exists {
				w(rw, `{"ssh_keys":[{"id":5,"name":"jauntbox-alice"}]}`)
			} else {
				w(rw, `{"ssh_keys":[]}`)
			}
			return
		}
		exists = true
		w(rw, `{"ssh_key":{"id":5,"name":"jauntbox-alice"}}`)
	})
	for i := 0; i < 2; i++ {
		id, err := h.EnsureSSHKey(context.Background(), "jauntbox-alice", "ssh-ed25519 AAAA", map[string]string{"role": "key"})
		if err != nil || id != 5 {
			t.Fatalf("id=%d err=%v", id, err)
		}
	}
	if n := len(f.posts("/ssh_keys")); n != 1 {
		t.Fatalf("posts = %d", n)
	}
}

func TestDeleteAndWaitDeleted(t *testing.T) {
	waitDeletedPoll = time.Millisecond
	gets := 0
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) {
		switch r.method {
		case "DELETE":
			w(rw, `{"action":{"id":1,"status":"running"}}`)
		case "GET":
			gets++
			if gets < 3 {
				w(rw, `{"server":{"id":9,"name":"x","status":"deleting"}}`)
				return
			}
			rw.WriteHeader(404)
			w(rw, `{"error":{"code":"not_found","message":"not found"}}`)
		}
	})
	ctx := context.Background()
	if err := h.DeleteBox(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0].method != "DELETE" || f.reqs[0].path != "/servers/9" {
		t.Fatalf("req = %+v", f.reqs[0])
	}
	if err := h.WaitDeleted(ctx, 9); err != nil || gets != 3 {
		t.Fatalf("err=%v gets=%d", err, gets)
	}
}

func TestWaitDeletedContextBound(t *testing.T) {
	waitDeletedPoll = time.Millisecond
	h, _ := newFake(t, func(r rec, rw http.ResponseWriter) { w(rw, `{"server":{"id":9,"name":"x"}}`) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.WaitDeleted(ctx, 9); err == nil {
		t.Fatal("expected error")
	}
}

func TestServerTypes(t *testing.T) {
	h, _ := newFake(t, func(r rec, rw http.ResponseWriter) {
		w(rw, `{"server_types":[
{"id":1,"name":"cpx31","cores":4,"memory":8,"architecture":"x86","prices":[{"location":"fsn1","price_hourly":{"net":"0.01","gross":"0.012"},"price_monthly":{"net":"10.00","gross":"11.90"}},{"location":"nbg1","price_hourly":{"net":"0.01","gross":"0.012"},"price_monthly":{"net":"12.00","gross":"14.28"}}],"locations":[{"id":1,"name":"fsn1","available":true},{"id":2,"name":"nbg1","available":false,"deprecation":{"announced":"2026-01-01T00:00:00Z","unavailable_after":"2026-12-01T00:00:00Z"}}]},
{"id":2,"name":"cax21","cores":4,"memory":8,"architecture":"arm","locations":[{"id":1,"name":"fsn1","available":true}],"deprecation":{"announced":"2026-01-01T00:00:00Z","unavailable_after":"2026-12-01T00:00:00Z"}}
],"meta":{"pagination":{"page":1,"per_page":50,"last_page":1,"total_entries":2}}}`)
	})
	ts, err := h.ServerTypes(context.Background())
	if err != nil || len(ts) != 2 {
		t.Fatalf("ts=%v err=%v", ts, err)
	}
	if a := ts[0]; a.Name != "cpx31" || a.Cores != 4 || a.MemoryGB != 8 || a.Arch != "amd64" || a.Deprecated || len(a.Locations) != 1 || a.Locations[0] != "fsn1" || a.PriceMonthly["fsn1"] != 11.90 || a.PriceMonthly["nbg1"] != 14.28 {
		t.Errorf("a = %+v", a)
	}
	if c := ts[1]; c.Arch != "arm64" || !c.Deprecated {
		t.Errorf("c = %+v", c)
	}
}

func TestSelfID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { w(rw, "123456\n") }))
	defer srv.Close()
	id, err := SelfID(context.Background(), srv.URL)
	if err != nil || id != 123456 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

func TestFindBoxRejectsUnsafeLabels(t *testing.T) {
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) { w(rw, `{"servers":[]}`) })
	for _, l := range []map[string]string{nil, {"jauntbox": "alice"}, {"role": "box"}, {"jauntbox": "alice", "role": "fw"}} {
		if _, err := h.FindBox(context.Background(), l); err == nil {
			t.Errorf("labels %v: expected error", l)
		}
	}
	if len(f.reqs) != 0 {
		t.Fatalf("made %d requests", len(f.reqs))
	}
}

func TestEnsureFirewallRequiresUserLabel(t *testing.T) {
	h, f := newFake(t, func(r rec, rw http.ResponseWriter) { w(rw, `{"firewalls":[]}`) })
	if _, err := h.EnsureFirewall(context.Background(), "jauntbox-alice", map[string]string{"role": "fw"}); err == nil {
		t.Fatal("expected error")
	}
	if len(f.reqs) != 0 {
		t.Fatalf("made %d requests", len(f.reqs))
	}
}

func TestDeleteBoxNotFoundIsSuccess(t *testing.T) {
	h, _ := newFake(t, func(r rec, rw http.ResponseWriter) {
		rw.WriteHeader(404)
		w(rw, `{"error":{"code":"not_found","message":"server not found"}}`)
	})
	if err := h.DeleteBox(context.Background(), 9); err != nil {
		t.Fatalf("err = %v, want nil for an already-gone server", err)
	}
}
