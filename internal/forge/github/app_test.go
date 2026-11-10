package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/forge"
)

func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func newTestApp(t *testing.T, meta AppMeta, h http.Handler) (*App, *rsa.PrivateKey, *httptest.Server) {
	t.Helper()
	k, p := testKey(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a, err := NewApp(meta, p, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return a, k, srv
}

var orgMeta = AppMeta{AppID: 42, Slug: "jauntbox-t", Owner: "WA-Systems-EU", OwnerIsOrg: true, InstallationID: 7}

func verifyJWT(t *testing.T, tok string, pub *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	dec := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("base64: %v", err)
		}
		return b
	}
	if got := string(dec(parts[0])); got != `{"alg":"RS256","typ":"JWT"}` {
		t.Fatalf("header = %s", got)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], dec(parts[2])); err != nil {
		t.Fatalf("signature: %v", err)
	}
	var c map[string]any
	if err := json.Unmarshal(dec(parts[1]), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestJWT(t *testing.T) {
	a, k, _ := newTestApp(t, orgMeta, http.NotFoundHandler())
	before := time.Now()
	tok, err := a.jwt()
	if err != nil {
		t.Fatal(err)
	}
	c := verifyJWT(t, tok, &k.PublicKey)
	if c["iss"] != "42" {
		t.Fatalf("iss = %v", c["iss"])
	}
	iat := int64(c["iat"].(float64))
	exp := int64(c["exp"].(float64))
	if d := before.Add(-60*time.Second).Unix() - iat; d < -2 || d > 2 {
		t.Fatalf("iat off by %d", d)
	}
	if exp-iat != 600 {
		t.Fatalf("exp-iat = %d, want 600 (now+9m minus now-60s)", exp-iat)
	}
}

func TestNewAppPKCS8(t *testing.T) {
	k, _ := testKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewApp(orgMeta, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := NewApp(orgMeta, []byte("garbage"), ""); err == nil {
		t.Fatal("garbage key accepted")
	}
}

func TestMintRequestsOneRepoAndExplicitPermissions(t *testing.T) {
	var a *App
	var k *rsa.PrivateKey
	a, k, _ = newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/app/installations/7/access_tokens" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		verifyJWT(t, jwt, &k.PublicKey)
		if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("headers: %v", r.Header)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		want := map[string]any{
			"repositories": []any{"web"},
			"permissions":  map[string]any{"contents": "write", "metadata": "read", "pull_requests": "write"},
		}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("body = %v", body)
		}
		fmt.Fprint(w, `{"token":"ghs_x","expires_at":"2026-11-10T12:00:00Z"}`)
	}))
	tok, err := a.Mint(context.Background(), "WA-Systems-EU/web")
	if err != nil {
		t.Fatal(err)
	}
	want := forge.Token{Value: "ghs_x", Repo: "WA-Systems-EU/web", ExpiresAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	if !tok.ExpiresAt.Equal(want.ExpiresAt) || tok.Value != want.Value || tok.Repo != want.Repo {
		t.Fatalf("token = %+v", tok)
	}
}

func TestMintRepoOfOtherOwnerRefused(t *testing.T) {
	var calls int32
	a, _, _ := newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	for _, r := range []string{"someone/web", "web", "WA-Systems-EU/"} {
		if _, err := a.Mint(context.Background(), r); err == nil {
			t.Fatalf("Mint(%q) accepted", r)
		}
	}
	if calls != 0 {
		t.Fatalf("%d HTTP calls", calls)
	}
}

func TestMintNotInstalled(t *testing.T) {
	a, _, _ := newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		fmt.Fprint(w, `{"message":"There is at least one repository that does not exist or is not accessible to the parent installation."}`)
	}))
	_, err := a.Mint(context.Background(), "WA-Systems-EU/web")
	if !errors.Is(err, forge.ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallationLookup(t *testing.T) {
	for _, tc := range []struct {
		meta AppMeta
		path string
	}{
		{AppMeta{AppID: 1, Slug: "s", Owner: "WA-Systems-EU", OwnerIsOrg: true}, "/orgs/WA-Systems-EU/installation"},
		{AppMeta{AppID: 1, Slug: "s", Owner: "thomas"}, "/users/thomas/installation"},
	} {
		var lookups int32
		a, _, _ := newTestApp(t, tc.meta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "GET" && r.URL.Path == tc.path:
				atomic.AddInt32(&lookups, 1)
				fmt.Fprint(w, `{"id":99}`)
			case r.Method == "POST" && r.URL.Path == "/app/installations/99/access_tokens":
				fmt.Fprint(w, `{"token":"ghs_y","expires_at":"2026-11-10T12:00:00Z"}`)
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
		}))
		for i := 0; i < 2; i++ {
			if _, err := a.Mint(context.Background(), tc.meta.Owner+"/web"); err != nil {
				t.Fatal(err)
			}
		}
		if lookups != 1 {
			t.Fatalf("lookups = %d, want 1 (cached)", lookups)
		}
	}

	a, _, _ := newTestApp(t, AppMeta{AppID: 1, Slug: "jauntbox-t", Owner: "thomas"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	_, err := a.Mint(context.Background(), "thomas/web")
	if err == nil || !strings.Contains(err.Error(), "https://github.com/apps/jauntbox-t/installations/new") {
		t.Fatalf("err = %v", err)
	}
}

func TestReposPaginatesAndRevokesListingToken(t *testing.T) {
	var revoked int32
	a, _, _ := newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/7/access_tokens":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !reflect.DeepEqual(body, map[string]any{"permissions": map[string]any{"metadata": "read"}}) {
				t.Errorf("listing mint body = %v", body)
			}
			fmt.Fprint(w, `{"token":"ghs_list","expires_at":"2026-11-10T12:00:00Z"}`)
		case r.Method == "GET" && r.URL.Path == "/installation/repositories":
			if r.Header.Get("Authorization") != "token ghs_list" {
				t.Errorf("auth = %q", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("per_page") != "100" {
				t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
			}
			n, start := 100, 0
			if r.URL.Query().Get("page") == "2" {
				n, start = 3, 100
			}
			var repos []string
			for i := start; i < start+n; i++ {
				br := "trunk"
				if i%2 == 1 {
					br = "master"
				}
				repos = append(repos, fmt.Sprintf(`{"name":"r%d","default_branch":"%s","clone_url":"https://github.com/WA-Systems-EU/r%d.git","owner":{"login":"WA-Systems-EU"}}`, i, br, i))
			}
			fmt.Fprintf(w, `{"total_count":103,"repositories":[%s]}`, strings.Join(repos, ","))
		case r.Method == "DELETE" && r.URL.Path == "/installation/token":
			if r.Header.Get("Authorization") != "token ghs_list" {
				t.Errorf("revoke auth = %q", r.Header.Get("Authorization"))
			}
			atomic.AddInt32(&revoked, 1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.String())
		}
	}))
	repos, err := a.Repos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 103 || revoked != 1 {
		t.Fatalf("repos=%d revoked=%d", len(repos), revoked)
	}
	if repos[0].DefaultBranch != "trunk" || repos[1].DefaultBranch != "master" {
		t.Fatalf("branches: %+v %+v", repos[0], repos[1])
	}
	if repos[2].Owner != "WA-Systems-EU" || repos[2].Name != "r2" || repos[2].CloneURL != "https://github.com/WA-Systems-EU/r2.git" {
		t.Fatalf("repo = %+v", repos[2])
	}
}

func TestReposRevokesEvenWhenListingFails(t *testing.T) {
	var revoked int32
	a, _, _ := newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			fmt.Fprint(w, `{"token":"ghs_list","expires_at":"2026-11-10T12:00:00Z"}`)
		case r.Method == "GET":
			w.WriteHeader(500)
		case r.Method == "DELETE":
			atomic.AddInt32(&revoked, 1)
			w.WriteHeader(500)
		}
	}))
	_, err := a.Repos(context.Background())
	if err == nil || !strings.Contains(err.Error(), "500") || revoked != 1 {
		t.Fatalf("err=%v revoked=%d", err, revoked)
	}
}

func TestReposRevokeFailureSurfacesWhenListingSucceeds(t *testing.T) {
	a, _, _ := newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			fmt.Fprint(w, `{"token":"ghs_list","expires_at":"2026-11-10T12:00:00Z"}`)
		case "GET":
			fmt.Fprint(w, `{"total_count":0,"repositories":[]}`)
		case "DELETE":
			w.WriteHeader(500)
		}
	}))
	if _, err := a.Repos(context.Background()); err == nil || !strings.Contains(err.Error(), "revoke") {
		t.Fatalf("err = %v", err)
	}
}

func TestRevoke(t *testing.T) {
	for _, code := range []int{204, 401} {
		a, _, _ := newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "DELETE" || r.URL.Path != "/installation/token" || r.Header.Get("Authorization") != "token ghs_x" {
				t.Errorf("unexpected %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			}
			w.WriteHeader(code)
		}))
		if err := a.Revoke(context.Background(), "ghs_x"); err != nil {
			t.Fatalf("code %d: %v", code, err)
		}
	}
	a, _, _ := newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	if err := a.Revoke(context.Background(), "ghs_x"); err == nil {
		t.Fatal("500 should error")
	}
}

func TestCheckPermissionsDrift(t *testing.T) {
	perms := `{"contents":"write","pull_requests":"write","metadata":"read"}`
	a, _, _ := newTestApp(t, orgMeta, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/app" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprintf(w, `{"permissions":%s}`, perms)
	}))
	if err := a.CheckPermissions(context.Background()); err != nil {
		t.Fatal(err)
	}
	perms = `{"contents":"write","pull_requests":"write","metadata":"read","workflows":"write"}`
	err := a.CheckPermissions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "workflows") {
		t.Fatalf("err = %v", err)
	}
	perms = `{"contents":"admin","pull_requests":"write","metadata":"read"}`
	err = a.CheckPermissions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "contents") {
		t.Fatalf("err = %v", err)
	}
	perms = `{"contents":"read","metadata":"read"}`
	if err := a.CheckPermissions(context.Background()); err != nil {
		t.Fatalf("narrower should pass: %v", err)
	}
}

func TestInstallURLAndGuarantees(t *testing.T) {
	a, _, _ := newTestApp(t, orgMeta, http.NotFoundHandler())
	if a.InstallURL() != "https://github.com/apps/jauntbox-t/installations/new" {
		t.Fatal(a.InstallURL())
	}
	if g := a.Guarantees(); !g.AutoExpiry || !g.WorkflowIsolation {
		t.Fatalf("%+v", g)
	}
	var _ forge.Forge = a
}
