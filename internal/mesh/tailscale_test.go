package mesh

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateAuthKeySendsEphemeralSingleUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/tailnet/-/keys" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tskey-api-k" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Capabilities struct {
				Devices struct {
					Create struct {
						Reusable, Ephemeral, Preauthorized bool
						Tags                               []string
					}
				}
			}
			ExpirySeconds int
		}
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		c := body.Capabilities.Devices.Create
		if c.Reusable || !c.Ephemeral || !c.Preauthorized || len(c.Tags) != 1 || c.Tags[0] != "tag:jauntbox-alice" {
			t.Errorf("create caps = %+v", c)
		}
		if body.ExpirySeconds != 3600 {
			t.Errorf("expirySeconds = %d", body.ExpirySeconds)
		}
		w.Write([]byte(`{"key":"tskey-auth-x"}`))
	}))
	defer srv.Close()
	k, err := NewTailscale("tskey-api-k", srv.URL, nil).CreateAuthKey(context.Background(), []string{"tag:jauntbox-alice"}, time.Hour)
	if err != nil || k != "tskey-auth-x" {
		t.Fatalf("k=%q err=%v", k, err)
	}
}

func TestCreateAuthKeyErrorIncludesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", 403)
	}))
	defer srv.Close()
	_, err := NewTailscale("k", srv.URL, nil).CreateAuthKey(context.Background(), nil, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "TAILSCALE_API_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceOnline(t *testing.T) {
	body := `{"devices":[
	 {"hostname":"other","name":"other.t.ts.net","connectedToControl":true},
	 {"hostname":"JauntBox-Alice","name":"jauntbox-alice.t.ts.net","connectedToControl":true}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tailnet/-/devices" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	m := NewTailscale("k", srv.URL, nil)
	if on, err := m.DeviceOnline(context.Background(), "jauntbox-alice"); err != nil || !on {
		t.Fatalf("on=%v err=%v", on, err)
	}
	if on, err := m.DeviceOnline(context.Background(), "jauntbox-bob"); err != nil || on {
		t.Fatalf("on=%v err=%v", on, err)
	}
}

func TestDeviceOnlineFallbackLastSeen(t *testing.T) {
	recent := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"devices":[{"hostname":"a","name":"a.t.ts.net","lastSeen":"` + recent + `"},{"hostname":"b","name":"b.t.ts.net","lastSeen":"` + old + `"}]}`))
	}))
	defer srv.Close()
	m := NewTailscale("k", srv.URL, nil)
	if on, _ := m.DeviceOnline(context.Background(), "a"); !on {
		t.Error("a should be online")
	}
	if on, _ := m.DeviceOnline(context.Background(), "b"); on {
		t.Error("b should be offline")
	}
}

func TestACLSnippetMentionsTag(t *testing.T) {
	s := ACLSnippet("alice")
	for _, w := range []string{"tag:jauntbox-alice", "autogroup:admin", `"alice"`, "accept"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in\n%s", w, s)
		}
	}
}
