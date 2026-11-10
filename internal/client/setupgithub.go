package client

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/forge/github"
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
)

// SetupGitHubOpts carries everything `jauntbox setup github` needs.
type SetupGitHubOpts struct {
	Cfg         config.Config
	Store       secrets.Store
	Admin       github.Admin
	AppAPIURL   string // API base for the App client ("" = github.com)
	WebURL      string // web base for the manifest form ("" = https://github.com)
	OpenBrowser func(url string) error
	Out         io.Writer
	InstallWait time.Duration // how long to wait for the installation (default 10 min)
	PollEvery   time.Duration // installation poll interval (default 5 s)
	Listen      func() (net.Listener, error)
	// Owner is the account that will own a new App (--owner); "" means ask.
	Owner string
	// PickOwner asks the human to choose; nil when there is no terminal.
	PickOwner func(choices []OwnerChoice, def int) (int, error)
}

// OwnerChoice is an account the App can be created under.
type OwnerChoice struct {
	Login string
	Org   bool
}

func (c OwnerChoice) String() string {
	if c.Org {
		return c.Login + " (organization)"
	}
	return c.Login + " (personal account)"
}

const defaultInstallWait = 10 * time.Minute

// AppManifest is the GitHub App manifest for the manifest flow.
func AppManifest(user, redirectURL string) []byte {
	b, _ := json.Marshal(map[string]any{
		"name":                "jauntbox-" + user,
		"url":                 "https://github.com/WA-Systems-EU/jaunt",
		"redirect_url":        redirectURL,
		"public":              false,
		"default_permissions": github.Permissions,
		"default_events":      []string{},
		"hook_attributes":     map[string]any{"url": "https://example.invalid/jauntbox", "active": false},
	})
	return b
}

// AdminToken finds the human's GitHub credential: `gh auth token`, else
// GITHUB_ADMIN_TOKEN. It is used once and never stored.
func AdminToken(lookPath func(string) (string, error), run func(argv ...string) ([]byte, error), getenv func(string) string) (string, error) {
	if _, err := lookPath("gh"); err == nil {
		if out, err := run("gh", "auth", "token"); err == nil {
			if t := strings.TrimSpace(string(out)); t != "" {
				return t, nil
			}
		}
	}
	if t := strings.TrimSpace(getenv("GITHUB_ADMIN_TOKEN")); t != "" {
		return t, nil
	}
	return "", errors.New("no GitHub admin credential: run `gh auth login` on this laptop or set GITHUB_ADMIN_TOKEN (used once, never stored)")
}

func (o SetupGitHubOpts) out() io.Writer {
	if o.Out == nil {
		return io.Discard
	}
	return o.Out
}

func (o SetupGitHubOpts) open(url string) {
	if o.OpenBrowser == nil {
		return
	}
	if err := o.OpenBrowser(url); err != nil {
		fmt.Fprintf(o.out(), "could not open a browser (%v); open the URL above by hand\n", err)
	}
}

func (o SetupGitHubOpts) loadApp() (*github.App, github.AppMeta, error) {
	pemKey, err := o.Store.Get(secrets.AppKeyFile)
	if err != nil {
		return nil, github.AppMeta{}, err
	}
	mb, err := o.Store.Get(secrets.AppMetaFile)
	if err != nil {
		return nil, github.AppMeta{}, err
	}
	var meta github.AppMeta
	if err := json.Unmarshal(mb, &meta); err != nil {
		return nil, github.AppMeta{}, fmt.Errorf("%s: %w", secrets.AppMetaFile, err)
	}
	app, err := github.NewApp(meta, pemKey, o.AppAPIURL)
	return app, meta, err
}

func (o SetupGitHubOpts) webBase() string {
	if w := strings.TrimRight(o.WebURL, "/"); w != "" {
		return w
	}
	return "https://github.com"
}

// appSettingsURL is where the owner manages (or deletes) the created App.
func (o SetupGitHubOpts) appSettingsURL(meta github.AppMeta) string {
	if meta.OwnerIsOrg {
		return o.webBase() + "/organizations/" + meta.Owner + "/settings/apps/" + meta.Slug
	}
	return o.webBase() + "/settings/apps/" + meta.Slug
}

// orphanErr explains how to recover an App that exists on GitHub but whose
// key is not in the store.
func (o SetupGitHubOpts) orphanErr(meta github.AppMeta, cause error) error {
	return fmt.Errorf("GitHub App %q exists but its private key is missing from %s%s: delete the app at %s "+
		"(or generate a new private key there and save it as %s with chmod 600), then re-run `jauntbox setup github`",
		meta.Slug, o.Store.Dir, causeSuffix(cause), o.appSettingsURL(meta), filepath.Join(o.Store.Dir, secrets.AppKeyFile))
}

func causeSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

func (o SetupGitHubOpts) haveApp() bool {
	_, e1 := o.Store.Get(secrets.AppKeyFile)
	_, e2 := o.Store.Get(secrets.AppMetaFile)
	return e1 == nil && e2 == nil
}

// SetupGitHub creates the App (manifest flow), waits for its installation,
// checks its permissions and applies the rulesets. Each step is skipped when
// already done, so a re-run resumes.
func SetupGitHub(ctx context.Context, o SetupGitHubOpts) error {
	out := o.out()
	if !o.haveApp() {
		if mb, err := o.Store.Get(secrets.AppMetaFile); err == nil {
			var meta github.AppMeta
			if json.Unmarshal(mb, &meta) == nil && meta.Slug != "" {
				return o.orphanErr(meta, nil)
			}
		}
		owner, err := o.chooseOwner(ctx)
		if err != nil {
			return err
		}
		if owner.Org {
			fmt.Fprintf(out, "creating the GitHub App jauntbox-%s under the organization %s\n", o.Cfg.User, owner.Login)
		} else {
			fmt.Fprintf(out, "creating the GitHub App jauntbox-%s under your personal account %s (only that account can install it)\n", o.Cfg.User, owner.Login)
		}
		if err := o.createApp(ctx, owner); err != nil {
			return err
		}
	}
	app, meta, err := o.loadApp()
	if err != nil {
		return err
	}
	if want := o.wantedOwner(); want != "" && !strings.EqualFold(want, meta.Owner) {
		return fmt.Errorf("the App %s already exists under %s, not %s; to move it, delete it at %s, remove %s and %s from %s, then re-run `jauntbox setup github`",
			meta.Slug, meta.Owner, want, o.appSettingsURL(meta), secrets.AppMetaFile, secrets.AppKeyFile, o.Store.Dir)
	}

	if meta.InstallationID == 0 {
		fmt.Fprintf(out, "install the app on the repositories agents may work on:\n  %s\n", app.InstallURL())
		o.open(app.InstallURL())
		id, err := o.waitInstalled(ctx, app)
		if err != nil {
			return err
		}
		meta.InstallationID = id
		mb, _ := json.Marshal(meta)
		if err := o.Store.Put(secrets.AppMetaFile, mb); err != nil {
			return err
		}
	}

	if err := app.CheckPermissions(ctx); err != nil {
		return err
	}
	repos, err := app.Repos(ctx)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		fmt.Fprintf(out, "the app is installed on no repositories yet; add some at %s and re-run\n", app.InstallURL())
	}
	for _, r := range repos {
		created, err := o.Admin.EnsureRuleset(ctx, r.Owner, r.Name, o.Cfg.GitHub.ProtectedPatterns)
		if err != nil {
			return fmt.Errorf("%s: %w", r.FullName(), err)
		}
		verb := "updated"
		if created {
			verb = "created"
		}
		fmt.Fprintf(out, "✓ %s (default branch %s) ruleset %s\n", r.FullName(), r.DefaultBranch, verb)
	}
	fmt.Fprintln(out, "token auto-expiry: yes (1 h)")
	fmt.Fprintln(out, "workflow isolation: yes (no workflows permission)")
	fmt.Fprintln(out, "next: run `jauntbox unlock` to push the App key to the box")
	return nil
}

// CheckGitHub verifies, without a browser or any write, that the App's
// permissions and every installed repository's ruleset are as expected.
func CheckGitHub(ctx context.Context, o SetupGitHubOpts) error {
	out := o.out()
	if !o.haveApp() {
		return errors.New("no GitHub App is set up yet; run `jauntbox setup github`")
	}
	app, meta, err := o.loadApp()
	if err != nil {
		return err
	}
	if meta.InstallationID == 0 {
		return fmt.Errorf("the app is not installed yet; run `jauntbox setup github` (install URL: %s)", app.InstallURL())
	}
	if err := app.CheckPermissions(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, "✓ app permissions within the allowed set")
	repos, err := app.Repos(ctx)
	if err != nil {
		return err
	}
	var bad []string
	for _, r := range repos {
		if err := o.Admin.VerifyRuleset(ctx, r.Owner, r.Name, o.Cfg.GitHub.ProtectedPatterns); err != nil {
			fmt.Fprintf(out, "✗ %s: %v\n", r.FullName(), err)
			bad = append(bad, r.FullName())
			continue
		}
		fmt.Fprintf(out, "✓ %s (default branch %s) ruleset ok\n", r.FullName(), r.DefaultBranch)
	}
	if len(bad) > 0 {
		return fmt.Errorf("ruleset drift on %s; run `jauntbox setup github` to repair", strings.Join(bad, ", "))
	}
	return nil
}

func (o SetupGitHubOpts) waitInstalled(ctx context.Context, app *github.App) (int64, error) {
	wait, every := o.InstallWait, o.PollEvery
	if wait <= 0 {
		wait = defaultInstallWait
	}
	if every <= 0 {
		every = 5 * time.Second
	}
	deadline := time.Now().Add(wait)
	var last error
	for {
		id, err := app.InstallationID(ctx)
		if err == nil {
			return id, nil
		}
		last = err
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if !time.Now().Add(every).Before(deadline) {
			return 0, fmt.Errorf("timed out after %s waiting for the app to be installed (%v); install it at %s, then re-run `jauntbox setup github`",
				wait, last, app.InstallURL())
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(every):
		}
	}
}

// wantedOwner is the owner asked for by --owner, else by github.org.
func (o SetupGitHubOpts) wantedOwner() string {
	if o.Owner != "" {
		return o.Owner
	}
	return o.Cfg.GitHub.Org
}

// chooseOwner settles which account a new App is created under: --owner, else
// a prompt over the admin user's own account and organizations (defaulting to
// github.org), else github.org without a terminal. With no terminal and nothing
// configured it refuses to guess: a private App can only ever be installed on
// the account that owns it.
func (o SetupGitHubOpts) chooseOwner(ctx context.Context) (OwnerChoice, error) {
	login, err := o.Admin.Login(ctx)
	if err != nil {
		return OwnerChoice{}, fmt.Errorf("checking the GitHub admin credential: %w", err)
	}
	choices := []OwnerChoice{{Login: login}}
	orgs, orgErr := o.Admin.Orgs(ctx)
	if orgErr != nil {
		fmt.Fprintf(o.out(), "warning: could not list your organizations (%v); only your personal account and github.org are offered\n", orgErr)
	}
	for _, org := range orgs {
		choices = append(choices, OwnerChoice{Login: org, Org: true})
	}
	find := func(name string) int {
		for i, c := range choices {
			if strings.EqualFold(c.Login, name) {
				return i
			}
		}
		return -1
	}
	if org := o.Cfg.GitHub.Org; org != "" && find(org) < 0 {
		choices = append(choices, OwnerChoice{Login: org, Org: true})
	}
	if o.Owner != "" {
		if i := find(o.Owner); i >= 0 {
			return choices[i], nil
		}
		if orgErr != nil { // cannot verify membership: trust the flag
			return OwnerChoice{Login: o.Owner, Org: true}, nil
		}
		names := make([]string, len(choices))
		for i, c := range choices {
			names[i] = c.Login
		}
		return OwnerChoice{}, fmt.Errorf("--owner %s: not your account or one of your organizations (%s)", o.Owner, strings.Join(names, ", "))
	}
	def := 0
	if org := o.Cfg.GitHub.Org; org != "" {
		def = find(org)
	}
	if len(choices) == 1 {
		return choices[0], nil
	}
	if o.PickOwner == nil {
		if o.Cfg.GitHub.Org != "" {
			return choices[def], nil
		}
		names := make([]string, len(choices))
		for i, c := range choices {
			names[i] = c.Login
		}
		return OwnerChoice{}, fmt.Errorf("the App could be created under %s; pass --owner <login> (or set github.org in the config)", strings.Join(names, ", "))
	}
	i, err := o.PickOwner(choices, def)
	if err != nil {
		return OwnerChoice{}, err
	}
	if i < 0 || i >= len(choices) {
		return OwnerChoice{}, fmt.Errorf("no such choice")
	}
	return choices[i], nil
}

// createApp runs the manifest flow through a short-lived loopback server.
func (o SetupGitHubOpts) createApp(ctx context.Context, owner OwnerChoice) error {
	out := o.out()
	listen := o.Listen
	if listen == nil {
		listen = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	}
	ln, err := listen()
	if err != nil {
		return err
	}
	var sb [16]byte
	if _, err := rand.Read(sb[:]); err != nil {
		ln.Close()
		return err
	}
	state := hex.EncodeToString(sb[:])
	base := "http://" + ln.Addr().String()
	web := o.webBase()
	target := web + "/settings/apps/new"
	if owner.Org {
		target = web + "/organizations/" + url.PathEscape(owner.Login) + "/settings/apps/new"
	}
	target += "?state=" + state
	manifest := AppManifest(o.Cfg.User, base+"/callback")

	done := make(chan error, 1)
	finish := func(err error) {
		select {
		case done <- err:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><body><p>Redirecting to GitHub to create the app...</p>
<form id="f" method="post" action="%s"><input type="hidden" name="manifest" value="%s"><noscript><button>Continue</button></noscript></form>
<script>document.getElementById("f").submit()</script></body></html>`, html.EscapeString(target), html.EscapeString(string(manifest)))
	})
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("state")
		if subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		// GitHub consumes the single-use code, so a dropped browser
		// connection must not cancel the conversion.
		cctx, ccancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer ccancel()
		meta, pemKey, err := o.Admin.ConvertManifest(cctx, code)
		if err != nil {
			http.Error(w, "could not finish creating the app; see the terminal", http.StatusInternalServerError)
			finish(fmt.Errorf("converting the app manifest: %w (if the app %q was created, delete it in GitHub's settings before re-running)", err, "jauntbox-"+o.Cfg.User))
			return
		}
		// Meta first, so a lone meta file marks "app exists, key missing".
		mb, _ := json.Marshal(meta)
		err = o.Store.Put(secrets.AppMetaFile, mb)
		if err == nil {
			err = o.Store.Put(secrets.AppKeyFile, pemKey)
		}
		if err != nil {
			err = o.orphanErr(meta, err)
			http.Error(w, "could not finish creating the app; see the terminal", http.StatusInternalServerError)
			finish(err)
			return
		}
		fmt.Fprintln(w, "done, you can close this tab")
		finish(nil)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()

	fmt.Fprintf(out, "creating the GitHub App; continue in your browser:\n  %s/\n", base)
	o.open(base + "/")

	wait := o.InstallWait
	if wait <= 0 {
		wait = defaultInstallWait
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return fmt.Errorf("timed out after %s waiting for the app to be created; if you complete the GitHub form later, the app %q will exist without its key here: delete it in GitHub's settings before re-running `jauntbox setup github`", wait, "jauntbox-"+o.Cfg.User)
	}
}

// ResolveListen turns the --listen value of `jauntbox setup github` into the
// address the manifest-flow server binds to. "" is loopback; "tailnet" is this
// machine's Tailscale IPv4 (for a browser on another tailnet device); anything
// else is an IP with an optional port. Wildcard addresses are refused: the
// address is also the redirect URL GitHub sends the browser to, so it must be
// one the browser can reach, and binding everywhere would expose the callback
// on every network this machine is on.
func ResolveListen(spec string, tailnetIP func() (string, error)) (string, error) {
	if spec == "" {
		return "127.0.0.1:0", nil
	}
	if spec == "tailnet" {
		ip, err := tailnetIP()
		if err != nil {
			return "", fmt.Errorf("finding this machine's tailnet IP: %w (pass --listen <ip> instead)", err)
		}
		spec = ip
	}
	host, port := spec, "0"
	if h, p, err := net.SplitHostPort(spec); err == nil {
		host, port = h, p
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("--listen %q: want an IP address, an IP:port, or \"tailnet\"", spec)
	}
	if ip.IsUnspecified() {
		return "", fmt.Errorf("--listen %q: a wildcard address is not a URL the browser can open, and it would expose the callback on every network; use \"tailnet\" or a specific IP", spec)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("--listen %q: port must be a number", spec)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// IsLoopbackListen reports whether a resolved listen address is loopback, in
// which case the browser on this machine can be opened directly.
func IsLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
