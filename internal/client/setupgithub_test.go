package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/forge/github"
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
)

type ghFake struct {
	t   *testing.T
	pem string

	mu          sync.Mutex
	conversions int
	installed   bool
	perms       string
	rulesets    map[string]int64 // owner/repo -> id
	rulesetCall int
	nextID      int64
	orgsFail    bool
	paywalled   map[string]bool // owner/repo -> rulesets need a paid plan
	convOwner   string          // owner GitHub reports for the new App ("" = WA-Systems-EU)
}

func newGHFake(t *testing.T) (*ghFake, *httptest.Server) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &ghFake{t: t, rulesets: map[string]int64{}, nextID: 100,
		perms: `{"contents":"write","pull_requests":"write","metadata":"read"}`,
		pem:   string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

var rulesetPathRE = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/rulesets(?:/(\d+))?$`)

func (f *ghFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == "GET" && p == "/user":
		fmt.Fprint(w, `{"login":"alice"}`)
	case r.Method == "GET" && p == "/user/orgs":
		if f.orgsFail {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"message":"Resource not accessible"}`)
			return
		}
		fmt.Fprint(w, `[{"login":"WA-Systems-EU"},{"login":"other-org"}]`)
	case r.Method == "POST" && p == "/app-manifests/c0de/conversions":
		f.conversions++
		pemJSON, _ := json.Marshal(f.pem)
		owner := f.convOwner
		if owner == "" {
			owner = "WA-Systems-EU"
		}
		fmt.Fprintf(w, `{"id":42,"slug":"jauntbox-alice","pem":%s,"owner":{"login":%q,"type":"Organization"}}`, pemJSON, owner)
	case r.Method == "GET" && p == "/app":
		fmt.Fprintf(w, `{"permissions":%s}`, f.perms)
	case r.Method == "GET" && p == "/orgs/WA-Systems-EU/installation":
		if !f.installed {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprint(w, `{"id":7}`)
	case r.Method == "POST" && p == "/app/installations/7/access_tokens":
		fmt.Fprint(w, `{"token":"ghs_x","expires_at":"2026-11-10T12:00:00Z"}`)
	case r.Method == "DELETE" && p == "/installation/token":
		w.WriteHeader(204)
	case r.Method == "GET" && p == "/installation/repositories":
		fmt.Fprint(w, `{"repositories":[`+
			`{"name":"web","default_branch":"main","clone_url":"https://x/web.git","owner":{"login":"WA-Systems-EU"}},`+
			`{"name":"api","default_branch":"trunk","clone_url":"https://x/api.git","owner":{"login":"WA-Systems-EU"}}]}`)
	case rulesetPathRE.MatchString(p):
		m := rulesetPathRE.FindStringSubmatch(p)
		repo := m[1]
		if f.paywalled[repo] {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"message":"Upgrade to GitHub Pro or make this repository public to enable this feature."}`)
			return
		}
		switch {
		case r.Method == "GET" && m[2] == "":
			if id, ok := f.rulesets[repo]; ok {
				fmt.Fprintf(w, `[{"id":%d,"name":%q}]`, id, github.RulesetName)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.Method == "POST":
			f.rulesetCall++
			f.nextID++
			f.rulesets[repo] = f.nextID
			w.WriteHeader(201)
			fmt.Fprint(w, `{}`)
		case r.Method == "PUT":
			f.rulesetCall++
			fmt.Fprint(w, `{}`)
		case r.Method == "GET":
			body := github.RulesetBody(nil)
			b, _ := json.Marshal(body)
			w.Write(b)
		}
	default:
		f.t.Errorf("unexpected %s %s", r.Method, p)
		w.WriteHeader(500)
	}
}

func (f *ghFake) setInstalled() { f.mu.Lock(); f.installed = true; f.mu.Unlock() }
func (f *ghFake) convs() int    { f.mu.Lock(); defer f.mu.Unlock(); return f.conversions }
func (f *ghFake) rsCalls() int  { f.mu.Lock(); defer f.mu.Unlock(); return f.rulesetCall }

var stateRE = regexp.MustCompile(`state=([0-9a-f]{32})`)
var actionRE = regexp.MustCompile(`action="([^"]+)"`)

// browser plays the human: for the local page it fetches it and calls the
// callback with the state from the form action (or a forged one); for the
// install URL it marks the app installed.
type browser struct {
	t        *testing.T
	fake     *ghFake
	state    string // "" = use the real one
	status   int    // callback status seen
	opened   []string
	pageHTML string
	action   string
	cancel   context.CancelFunc
	probe    func(u string) // runs against the local page before the flow continues
}

func (b *browser) open(u string) error {
	b.opened = append(b.opened, u)
	if strings.HasPrefix(u, "http://127.0.0.1:") {
		if b.probe != nil {
			b.probe(u)
		}
		resp, err := http.Get(u)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		b.pageHTML = string(body)
		m := actionRE.FindStringSubmatch(b.pageHTML)
		if m == nil {
			b.t.Fatalf("no form action in page: %s", b.pageHTML)
		}
		b.action = strings.ReplaceAll(m[1], "&amp;", "&")
		st := stateRE.FindStringSubmatch(b.action)[1]
		if b.state != "" {
			st = b.state
		}
		cb, err := http.Get(strings.TrimSuffix(u, "/") + "/callback?code=c0de&state=" + st)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, cb.Body)
		cb.Body.Close()
		b.status = cb.StatusCode
		if b.cancel != nil && cb.StatusCode != 200 {
			b.cancel()
		}
		return nil
	}
	if strings.Contains(u, "/installations/new") {
		b.fake.setInstalled()
	}
	return nil
}

func setupOpts(t *testing.T, f *ghFake, srv *httptest.Server, br *browser) (SetupGitHubOpts, *bytes.Buffer) {
	t.Helper()
	cfg := config.Default()
	cfg.User = "alice"
	cfg.GitHub.Org = "WA-Systems-EU"
	out := &bytes.Buffer{}
	return SetupGitHubOpts{
		Cfg:         cfg,
		Store:       secrets.Store{Dir: filepath.Join(t.TempDir(), "secrets")},
		Admin:       github.Admin{Token: "admin-secret", APIURL: srv.URL},
		AppAPIURL:   srv.URL,
		WebURL:      "https://web.test",
		OpenBrowser: br.open,
		Out:         out,
		InstallWait: 2 * time.Second,
		PollEvery:   10 * time.Millisecond,
	}, out
}

func TestSetupGitHubCreatesAppOnce(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, out := setupOpts(t, f, srv, br)
	if err := SetupGitHub(context.Background(), o); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if !strings.HasPrefix(br.action, "https://web.test/organizations/WA-Systems-EU/settings/apps/new?state=") {
		t.Fatalf("form action = %q", br.action)
	}
	if !strings.Contains(br.pageHTML, `name="manifest"`) {
		t.Fatalf("page lacks manifest field: %s", br.pageHTML)
	}
	if _, err := o.Store.Get(secrets.AppKeyFile); err != nil {
		t.Fatal(err)
	}
	mb, err := o.Store.Get(secrets.AppMetaFile)
	if err != nil {
		t.Fatal(err)
	}
	var meta github.AppMeta
	if err := json.Unmarshal(mb, &meta); err != nil || meta.InstallationID != 7 || meta.AppID != 42 || !meta.OwnerIsOrg {
		t.Fatalf("meta = %+v err=%v", meta, err)
	}
	fi, _ := os.Stat(filepath.Join(o.Store.Dir, secrets.AppKeyFile))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("pem mode %v", fi.Mode())
	}
	got := out.String()
	for _, want := range []string{
		"https://github.com/apps/jauntbox-alice/installations/new",
		"✓ WA-Systems-EU/web (default branch main) ruleset created",
		"✓ WA-Systems-EU/api (default branch trunk) ruleset created",
		"token auto-expiry: yes (1 h)",
		"workflow isolation: yes (no workflows permission)",
		"next: run `jauntbox unlock` to push the App key to the box",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "admin-secret") || strings.Contains(got, "BEGIN RSA") {
		t.Fatalf("output leaks a secret:\n%s", got)
	}

	// Second run: nothing to create, nothing to wait for.
	br2 := &browser{t: t, fake: f}
	o.OpenBrowser = br2.open
	out.Reset()
	if err := SetupGitHub(context.Background(), o); err != nil {
		t.Fatalf("second setup: %v", err)
	}
	if f.convs() != 1 {
		t.Fatalf("conversions = %d, want 1", f.convs())
	}
	if len(br2.opened) != 0 {
		t.Fatalf("second run opened %v", br2.opened)
	}
	if !strings.Contains(out.String(), "ruleset updated") {
		t.Fatalf("second run output:\n%s", out)
	}
}

func TestSetupGitHubPersonalAccountFormURL(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, _ := setupOpts(t, f, srv, br)
	o.Cfg.GitHub.Org = ""
	o.Owner = "alice"
	// The personal-account path ends in the same fake (owner comes from the conversion),
	// so stop after the page is served: cancel once the form has been read.
	ctx, cancel := context.WithCancel(context.Background())
	br.cancel = cancel
	br.state = "00000000000000000000000000000000"
	_ = SetupGitHub(ctx, o)
	if !strings.HasPrefix(br.action, "https://web.test/settings/apps/new?state=") {
		t.Fatalf("form action = %q", br.action)
	}
}

func TestSetupGitHubWrongStateRejected(t *testing.T) {
	f, srv := newGHFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br := &browser{t: t, fake: f, state: "ffffffffffffffffffffffffffffffff", cancel: cancel}
	o, _ := setupOpts(t, f, srv, br)
	err := SetupGitHub(ctx, o)
	if err == nil {
		t.Fatal("want error after cancel")
	}
	if br.status != 400 {
		t.Fatalf("callback status = %d, want 400", br.status)
	}
	if f.convs() != 0 {
		t.Fatalf("conversions = %d, want 0", f.convs())
	}
	if _, err := o.Store.Get(secrets.AppKeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pem stored: %v", err)
	}
	if _, err := o.Store.Get(secrets.AppMetaFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("meta stored: %v", err)
	}
}

func TestSetupGitHubAppliesRulesetPerRepo(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, out := setupOpts(t, f, srv, br)
	if err := SetupGitHub(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if f.rsCalls() != 2 {
		t.Fatalf("ruleset writes = %d, want 2", f.rsCalls())
	}
	if !strings.Contains(out.String(), "(default branch main)") || !strings.Contains(out.String(), "(default branch trunk)") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestSetupGitHubPermissionDrift(t *testing.T) {
	f, srv := newGHFake(t)
	f.perms = `{"contents":"write","pull_requests":"write","metadata":"read","workflows":"write"}`
	br := &browser{t: t, fake: f}
	o, _ := setupOpts(t, f, srv, br)
	err := SetupGitHub(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "workflows") {
		t.Fatalf("err = %v", err)
	}
	if f.rsCalls() != 0 {
		t.Fatalf("ruleset writes = %d, want 0", f.rsCalls())
	}
}

func TestSetupGitHubInstallTimeout(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, _ := setupOpts(t, f, srv, br)
	o.InstallWait = 60 * time.Millisecond
	o.OpenBrowser = func(u string) error {
		if strings.Contains(u, "/installations/new") {
			return errors.New("no browser") // also: open failure is only printed
		}
		return br.open(u)
	}
	err := SetupGitHub(context.Background(), o)
	if err == nil ||
		!strings.Contains(err.Error(), "https://github.com/apps/jauntbox-alice/installations/new") ||
		!strings.Contains(err.Error(), "re-run `jauntbox setup github`") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckGitHub(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, out := setupOpts(t, f, srv, br)
	if err := CheckGitHub(context.Background(), o); err == nil || !strings.Contains(err.Error(), "jauntbox setup github") {
		t.Fatalf("check before setup: %v", err)
	}
	if err := SetupGitHub(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	before := f.rsCalls()
	out.Reset()
	// The fake serves the exact desired ruleset, so verification passes.
	if err := CheckGitHub(context.Background(), o); err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	if f.rsCalls() != before {
		t.Fatal("check wrote rulesets")
	}
	if !strings.Contains(out.String(), "WA-Systems-EU/web") || !strings.Contains(out.String(), "WA-Systems-EU/api") {
		t.Fatalf("output:\n%s", out)
	}
	f.perms = `{"administration":"write"}`
	if err := CheckGitHub(context.Background(), o); err == nil {
		t.Fatal("want permission drift error")
	}
}

func TestAppManifest(t *testing.T) {
	got := AppManifest("alice", "http://127.0.0.1:1234/callback")
	want := `{"name":"jauntbox-alice","url":"https://github.com/WA-Systems-EU/jaunt","redirect_url":"http://127.0.0.1:1234/callback","public":false,` +
		`"default_permissions":{"contents":"write","metadata":"read","pull_requests":"write"},"default_events":[],` +
		`"hook_attributes":{"url":"https://example.invalid/jauntbox","active":false}}`
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if string(ga) != string(gb) {
		t.Fatalf("manifest:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(string(got), "workflows") {
		t.Fatal("manifest mentions workflows")
	}
}

func TestAdminTokenOrder(t *testing.T) {
	look := func(ok bool) func(string) (string, error) {
		return func(n string) (string, error) {
			if ok {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		}
	}
	run := func(argv ...string) ([]byte, error) {
		if strings.Join(argv, " ") != "gh auth token" {
			t.Errorf("argv = %v", argv)
		}
		return []byte("gho_fromgh\n"), nil
	}
	env := func(k string) string {
		if k == "GITHUB_ADMIN_TOKEN" {
			return "ghp_fromenv"
		}
		return ""
	}
	if tok, err := AdminToken(look(true), run, env); err != nil || tok != "gho_fromgh" {
		t.Fatalf("gh present: %q %v", tok, err)
	}
	if tok, err := AdminToken(look(false), run, env); err != nil || tok != "ghp_fromenv" {
		t.Fatalf("gh missing: %q %v", tok, err)
	}
	failRun := func(...string) ([]byte, error) { return nil, errors.New("not logged in") }
	if tok, err := AdminToken(look(true), failRun, env); err != nil || tok != "ghp_fromenv" {
		t.Fatalf("gh fails: %q %v", tok, err)
	}
	_, err := AdminToken(look(false), run, func(string) string { return "" })
	want := "no GitHub admin credential: run `gh auth login` on this laptop or set GITHUB_ADMIN_TOKEN (used once, never stored)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
}

func TestSetupGitHubStoreFailureExplainsOrphanedApp(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, _ := setupOpts(t, f, srv, br)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	o.Store = secrets.Store{Dir: filepath.Join(blocker, "secrets")} // Put cannot create the dir
	err := SetupGitHub(context.Background(), o)
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"jauntbox-alice", "https://web.test/organizations/WA-Systems-EU/settings/apps/jauntbox-alice", "delete"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q: %v", want, err)
		}
	}
}

func TestSetupGitHubLoneMetaReportsMissingKey(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, _ := setupOpts(t, f, srv, br)
	mb, _ := json.Marshal(github.AppMeta{AppID: 42, Slug: "jauntbox-alice", Owner: "WA-Systems-EU", OwnerIsOrg: true})
	if err := o.Store.Put(secrets.AppMetaFile, mb); err != nil {
		t.Fatal(err)
	}
	err := SetupGitHub(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "key is missing") ||
		!strings.Contains(err.Error(), "https://web.test/organizations/WA-Systems-EU/settings/apps/jauntbox-alice") {
		t.Fatalf("err = %v", err)
	}
	if len(br.opened) != 0 || f.convs() != 0 {
		t.Fatalf("started a new flow: opened=%v convs=%d", br.opened, f.convs())
	}
}

func TestResolveListen(t *testing.T) {
	ts := func() (string, error) { return "100.101.102.103", nil }
	cases := []struct{ spec, want string }{
		{"", "127.0.0.1:0"},
		{"tailnet", "100.101.102.103:0"},
		{"100.64.0.7", "100.64.0.7:0"},
		{"100.64.0.7:8765", "100.64.0.7:8765"},
		{"fd7a:115c:a1e0::1", "[fd7a:115c:a1e0::1]:0"},
		{"[fd7a:115c:a1e0::1]:9000", "[fd7a:115c:a1e0::1]:9000"},
	}
	for _, c := range cases {
		got, err := ResolveListen(c.spec, ts)
		if err != nil || got != c.want {
			t.Errorf("ResolveListen(%q) = %q, %v; want %q", c.spec, got, err, c.want)
		}
	}
	for _, bad := range []string{"0.0.0.0", "0.0.0.0:80", "::", "[::]:0", "my-host", "100.64.0.7:http"} {
		if got, err := ResolveListen(bad, ts); err == nil {
			t.Errorf("ResolveListen(%q) = %q, want an error", bad, got)
		}
	}
	if _, err := ResolveListen("tailnet", func() (string, error) { return "", errors.New("tailscale not running") }); err == nil || !strings.Contains(err.Error(), "tailscale not running") {
		t.Errorf("tailnet lookup error not surfaced: %v", err)
	}
}

func TestIsLoopbackListen(t *testing.T) {
	if !IsLoopbackListen("127.0.0.1:0") || IsLoopbackListen("100.64.0.7:0") {
		t.Error("IsLoopbackListen wrong")
	}
}

// formOwner runs setup until the GitHub form has been read and returns its action.
func formOwner(t *testing.T, mutate func(*SetupGitHubOpts)) (string, string, error) {
	t.Helper()
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, out := setupOpts(t, f, srv, br)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br.cancel = cancel
	br.state = "00000000000000000000000000000000"
	mutate(&o)
	err := SetupGitHub(ctx, o)
	return br.action, out.String(), err
}

func TestSetupGitHubOwnerPrompt(t *testing.T) {
	var seen []OwnerChoice
	var seenDef int
	action, out, _ := formOwner(t, func(o *SetupGitHubOpts) {
		o.Cfg.GitHub.Org = "WA-Systems-EU"
		o.PickOwner = func(c []OwnerChoice, def int) (int, error) { seen, seenDef = c, def; return 2, nil }
	})
	want := []OwnerChoice{{"alice", false}, {"WA-Systems-EU", true}, {"other-org", true}}
	if fmt.Sprint(seen) != fmt.Sprint(want) || seenDef != 1 {
		t.Fatalf("choices %v def %d", seen, seenDef)
	}
	if !strings.HasPrefix(action, "https://web.test/organizations/other-org/settings/apps/new?state=") {
		t.Fatalf("form action = %q", action)
	}
	if !strings.Contains(out, "under the organization other-org") {
		t.Fatalf("output lacks the owner:\n%s", out)
	}
}

func TestSetupGitHubOwnerPromptDefaultsToPersonal(t *testing.T) {
	var seenDef = -1
	action, out, _ := formOwner(t, func(o *SetupGitHubOpts) {
		o.Cfg.GitHub.Org = ""
		o.PickOwner = func(c []OwnerChoice, def int) (int, error) { seenDef = def; return def, nil }
	})
	if seenDef != 0 || !strings.HasPrefix(action, "https://web.test/settings/apps/new?state=") {
		t.Fatalf("def %d action %q", seenDef, action)
	}
	if !strings.Contains(out, "under your personal account alice") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestSetupGitHubOwnerFlag(t *testing.T) {
	action, _, _ := formOwner(t, func(o *SetupGitHubOpts) {
		o.Owner = "wa-systems-eu"
		o.PickOwner = func([]OwnerChoice, int) (int, error) { t.Fatal("prompted despite --owner"); return 0, nil }
	})
	if !strings.HasPrefix(action, "https://web.test/organizations/WA-Systems-EU/settings/apps/new?state=") {
		t.Fatalf("form action = %q", action)
	}
	_, _, err := formOwner(t, func(o *SetupGitHubOpts) { o.Owner = "nope" })
	if err == nil || !strings.Contains(err.Error(), "alice, WA-Systems-EU, other-org") {
		t.Fatalf("unknown owner err = %v", err)
	}
}

func TestSetupGitHubOwnerNonInteractive(t *testing.T) {
	// Configured org: used without asking.
	action, _, _ := formOwner(t, func(o *SetupGitHubOpts) {})
	if !strings.HasPrefix(action, "https://web.test/organizations/WA-Systems-EU/") {
		t.Fatalf("form action = %q", action)
	}
	// Nothing configured, orgs exist, no terminal: refuse to guess.
	action, _, err := formOwner(t, func(o *SetupGitHubOpts) { o.Cfg.GitHub.Org = "" })
	if err == nil || !strings.Contains(err.Error(), "--owner") || action != "" {
		t.Fatalf("err = %v action = %q", err, action)
	}
}

func TestSetupGitHubOwnerOrgsUnlisted(t *testing.T) {
	// Without read:org the org list fails: the configured org is still offered.
	f, srv := newGHFake(t)
	f.orgsFail = true
	br := &browser{t: t, fake: f}
	o, out := setupOpts(t, f, srv, br)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	br.cancel = cancel
	br.state = "00000000000000000000000000000000"
	var seen []OwnerChoice
	o.PickOwner = func(c []OwnerChoice, def int) (int, error) { seen = c; return def, nil }
	_ = SetupGitHub(ctx, o)
	if fmt.Sprint(seen) != fmt.Sprint([]OwnerChoice{{"alice", false}, {"WA-Systems-EU", true}}) {
		t.Fatalf("choices %v", seen)
	}
	if !strings.Contains(out.String(), "could not list your organizations") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestSetupGitHubExistingAppOtherOwner(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, _ := setupOpts(t, f, srv, br)
	if err := SetupGitHub(context.Background(), o); err != nil { // App under WA-Systems-EU
		t.Fatal(err)
	}
	o.Owner = "other-org"
	err := SetupGitHub(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "already exists under WA-Systems-EU") || !strings.Contains(err.Error(), secrets.AppKeyFile) {
		t.Fatalf("err = %v", err)
	}
}

func paywalledSetup(t *testing.T, mutate func(*SetupGitHubOpts)) (SetupGitHubOpts, *bytes.Buffer, error) {
	t.Helper()
	f, srv := newGHFake(t)
	f.paywalled = map[string]bool{"WA-Systems-EU/api": true}
	br := &browser{t: t, fake: f}
	o, out := setupOpts(t, f, srv, br)
	mutate(&o)
	err := SetupGitHub(context.Background(), o)
	return o, out, err
}

func TestSetupGitHubUnprotectedRefusedByDefault(t *testing.T) {
	_, out, err := paywalledSetup(t, func(*SetupGitHubOpts) {})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"WA-Systems-EU/api", "GitHub Team", "--allow-unprotected", "allow_unprotected"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	got := out.String()
	if !strings.Contains(got, "✓ WA-Systems-EU/web (default branch main) ruleset created") {
		t.Errorf("protectable repo skipped:\n%s", got)
	}
	if strings.Contains(got, "next: run `jauntbox unlock`") {
		t.Errorf("printed next step despite failure:\n%s", got)
	}
}

func TestSetupGitHubUnprotectedAllowed(t *testing.T) {
	for name, allow := range map[string]func(*SetupGitHubOpts){
		"flag":   func(o *SetupGitHubOpts) { o.AllowUnprotected = true },
		"config": func(o *SetupGitHubOpts) { o.Cfg.GitHub.AllowUnprotected = true },
	} {
		o, out, err := paywalledSetup(t, allow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := out.String()
		for _, want := range []string{
			"⚠ WA-Systems-EU/api (default branch trunk) NOT protected",
			"agents can push to the default branch of: WA-Systems-EU/api",
			"next: run `jauntbox unlock`",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: output lacks %q:\n%s", name, want, got)
			}
		}
		out.Reset()
		if err := CheckGitHub(context.Background(), o); err != nil {
			t.Fatalf("%s: check: %v", name, err)
		}
		if !strings.Contains(out.String(), "⚠ WA-Systems-EU/api") {
			t.Errorf("%s: check output:\n%s", name, out)
		}
	}
}

func TestCheckGitHubUnprotectedFails(t *testing.T) {
	o, out, _ := paywalledSetup(t, func(o *SetupGitHubOpts) { o.AllowUnprotected = true })
	o.AllowUnprotected = false
	out.Reset()
	err := CheckGitHub(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "WA-Systems-EU/api") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestSetupGitHubPromptPickOtherThanConfigCompletes(t *testing.T) {
	// github.org names one org, the human picks another: setup must finish,
	// and a re-run must not reject the App for living elsewhere than github.org.
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, out := setupOpts(t, f, srv, br)
	o.Cfg.GitHub.Org = "other-org"
	o.PickOwner = func(c []OwnerChoice, def int) (int, error) { return 1, nil } // WA-Systems-EU
	if err := SetupGitHub(context.Background(), o); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	out.Reset()
	if err := SetupGitHub(context.Background(), o); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if !strings.Contains(out.String(), "warning: the App jauntbox-alice is owned by WA-Systems-EU, not github.org (other-org)") {
		t.Fatalf("re-run output:\n%s", out)
	}
}

func TestSetupGitHubLocalPageNeedsSecretPathAndHost(t *testing.T) {
	f, srv := newGHFake(t)
	br := &browser{t: t, fake: f}
	o, out := setupOpts(t, f, srv, br)
	var root, wrongHost, bareCallback int
	br.probe = func(u string) {
		pu, err := neturl.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^/[0-9a-f]{32}/$`).MatchString(pu.Path) {
			t.Errorf("page path %q lacks a secret component", pu.Path)
		}
		get := func(target, host string) int {
			req, _ := http.NewRequest("GET", target, nil)
			if host != "" {
				req.Host = host
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return resp.StatusCode
		}
		root = get("http://"+pu.Host+"/", "")
		bareCallback = get("http://"+pu.Host+"/callback?code=c0de&state=x", "")
		wrongHost = get(u, "attacker.example:80")
	}
	if err := SetupGitHub(context.Background(), o); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if root != 404 || bareCallback != 404 || wrongHost != 403 {
		t.Fatalf("root=%d bareCallback=%d wrongHost=%d", root, bareCallback, wrongHost)
	}
}

func TestSetupGitHubConvertedOwnerMustMatchChoice(t *testing.T) {
	f, srv := newGHFake(t)
	f.convOwner = "mallory"
	br := &browser{t: t, fake: f}
	o, _ := setupOpts(t, f, srv, br)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := SetupGitHub(ctx, o)
	if err == nil || !strings.Contains(err.Error(), "under mallory, not WA-Systems-EU") {
		t.Fatalf("err = %v", err)
	}
	if _, err := o.Store.Get(secrets.AppMetaFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("meta stored for a foreign App: %v", err)
	}
	if _, err := o.Store.Get(secrets.AppKeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key stored for a foreign App: %v", err)
	}
}

func TestHostMatches(t *testing.T) {
	cases := []struct {
		req, bound string
		want       bool
	}{
		{"100.64.0.7:8765", "100.64.0.7:8765", true},
		{"100.64.0.7", "100.64.0.7:80", true}, // browsers drop the default port
		{"100.64.0.7:80", "100.64.0.7:80", true},
		{"[fd7a::1]", "[fd7a::1]:80", true},
		{"100.64.0.7", "100.64.0.7:8765", false},
		{"attacker.example:8765", "100.64.0.7:8765", false},
		{"attacker.example", "100.64.0.7:80", false},
	}
	for _, c := range cases {
		if got := hostMatches(c.req, c.bound); got != c.want {
			t.Errorf("hostMatches(%q, %q) = %v", c.req, c.bound, got)
		}
	}
}
