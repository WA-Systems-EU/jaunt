// Package github implements forge.Forge with a GitHub App, minting
// installation tokens scoped to a single repository.
package github

import (
	"bytes"
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
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/forge"
)

const defaultAPIURL = "https://api.github.com"

// Permissions is the exact permission set the App may hold and every mint requests.
var Permissions = map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}

// AppMeta identifies the GitHub App and where it is installed.
type AppMeta struct {
	AppID          int64  `json:"app_id"`
	Slug           string `json:"slug"`
	Owner          string `json:"owner"`
	OwnerIsOrg     bool   `json:"owner_is_org"`
	InstallationID int64  `json:"installation_id,omitempty"`
}

// App is a GitHub App client authenticated with its private key.
type App struct {
	meta   AppMeta
	key    *rsa.PrivateKey
	apiURL string
	hc     *http.Client
	mu     sync.Mutex
	instID int64
}

var _ forge.Forge = (*App)(nil)

// NewApp parses the PEM key (PKCS#1 or PKCS#8 RSA). Empty apiURL means github.com.
func NewApp(meta AppMeta, pemKey []byte, apiURL string) (*App, error) {
	blk, _ := pem.Decode(pemKey)
	if blk == nil {
		return nil, errors.New("github app key: no PEM block found")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		key = k
	} else {
		pk, err2 := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("github app key: not a PKCS#1 or PKCS#8 key: %w", err2)
		}
		rk, ok := pk.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("github app key: not an RSA key")
		}
		key = rk
	}
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	return &App{
		meta:   meta,
		key:    key,
		apiURL: strings.TrimRight(apiURL, "/"),
		hc:     &http.Client{Timeout: 10 * time.Second},
		instID: meta.InstallationID,
	}, nil
}

// Guarantees: installation tokens expire on their own and cannot touch workflows.
func (a *App) Guarantees() forge.Guarantees {
	return forge.Guarantees{AutoExpiry: true, WorkflowIsolation: true}
}

// InstallURL is where the owner installs the App on repositories.
func (a *App) InstallURL() string {
	return "https://github.com/apps/" + a.meta.Slug + "/installations/new"
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (a *App) jwt() (string, error) {
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"iss": strconv.FormatInt(a.meta.AppID, 10),
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}
	in := b64([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64(claims)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return in + "." + b64(sig), nil
}

// apiError is a non-2xx answer from the API.
type apiError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("github %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// do performs a request. auth is the full Authorization header value.
func (a *App) do(ctx context.Context, method, path, auth string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.apiURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return fmt.Errorf("github %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(data))
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &m) == nil && m.Message != "" {
			msg = m.Message
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return &apiError{Method: method, Path: path, Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("github %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

func (a *App) appDo(ctx context.Context, method, path string, body, out any) error {
	j, err := a.jwt()
	if err != nil {
		return err
	}
	return a.do(ctx, method, path, "Bearer "+j, body, out)
}

func (a *App) installationID(ctx context.Context) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.instID != 0 {
		return a.instID, nil
	}
	path := "/users/" + a.meta.Owner + "/installation"
	if a.meta.OwnerIsOrg {
		path = "/orgs/" + a.meta.Owner + "/installation"
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := a.appDo(ctx, "GET", path, nil, &out); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == 404 {
			return 0, fmt.Errorf("the app is not installed on %s; install it at %s: %w", a.meta.Owner, a.InstallURL(), err)
		}
		return 0, err
	}
	if out.ID == 0 {
		return 0, fmt.Errorf("installation lookup for %s returned no id; install the app at %s", a.meta.Owner, a.InstallURL())
	}
	a.instID = out.ID
	return out.ID, nil
}

type tokenResp struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *App) mint(ctx context.Context, repositories []string, perms map[string]string) (tokenResp, error) {
	id, err := a.installationID(ctx)
	if err != nil {
		return tokenResp{}, err
	}
	body := map[string]any{"permissions": perms}
	if repositories != nil {
		body["repositories"] = repositories
	}
	var out tokenResp
	err = a.appDo(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", id), body, &out)
	if err != nil {
		return tokenResp{}, err
	}
	return out, nil
}

// Mint returns a token limited to one repository (owner/name) with Permissions.
func (a *App) Mint(ctx context.Context, repo string) (forge.Token, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || name == "" || strings.Contains(name, "/") || !strings.EqualFold(owner, a.meta.Owner) {
		return forge.Token{}, fmt.Errorf("repository %q is not of the form %s/<name>", repo, a.meta.Owner)
	}
	out, err := a.mint(ctx, []string{name}, Permissions)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == 422 && strings.Contains(ae.Message, "does not exist or is not accessible") {
			return forge.Token{}, fmt.Errorf("%s: install the app on it at %s: %w", repo, a.InstallURL(), forge.ErrNotInstalled)
		}
		return forge.Token{}, err
	}
	return forge.Token{Value: out.Token, Repo: a.meta.Owner + "/" + name, ExpiresAt: out.ExpiresAt}, nil
}

// Revoke invalidates an installation token. An already-expired token (401) is fine.
func (a *App) Revoke(ctx context.Context, token string) error {
	err := a.do(ctx, "DELETE", "/installation/token", "token "+token, nil, nil)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == 401 {
		return nil
	}
	return err
}

// Repos lists the repositories the App is installed on, using a metadata-only
// token that is revoked right after.
func (a *App) Repos(ctx context.Context) (repos []forge.Repo, err error) {
	tok, err := a.mint(ctx, nil, map[string]string{"metadata": "read"})
	if err != nil {
		return nil, err
	}
	defer func() {
		if rerr := a.Revoke(context.WithoutCancel(ctx), tok.Token); rerr != nil && err == nil {
			repos, err = nil, fmt.Errorf("revoke listing token: %w", rerr)
		}
	}()
	for page := 1; ; page++ {
		var out struct {
			Repositories []struct {
				Name          string `json:"name"`
				DefaultBranch string `json:"default_branch"`
				CloneURL      string `json:"clone_url"`
				Owner         struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repositories"`
		}
		path := fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page)
		if err := a.do(ctx, "GET", path, "token "+tok.Token, nil, &out); err != nil {
			return nil, err
		}
		for _, r := range out.Repositories {
			repos = append(repos, forge.Repo{Owner: r.Owner.Login, Name: r.Name, DefaultBranch: r.DefaultBranch, CloneURL: r.CloneURL})
		}
		if len(out.Repositories) < 100 {
			return repos, nil
		}
	}
}

var permLevel = map[string]int{"read": 1, "write": 2, "admin": 3}

// CheckPermissions fails if the App holds any permission outside Permissions
// or above its level.
func (a *App) CheckPermissions(ctx context.Context) error {
	var out struct {
		Permissions map[string]string `json:"permissions"`
	}
	if err := a.appDo(ctx, "GET", "/app", nil, &out); err != nil {
		return err
	}
	var bad []string
	for name, level := range out.Permissions {
		allowed, ok := Permissions[name]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s:%s (not allowed)", name, level))
		case permLevel[level] > permLevel[allowed]:
			bad = append(bad, fmt.Sprintf("%s:%s (max %s)", name, level, allowed))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("github app has permissions beyond the allowed set: %s", strings.Join(bad, ", "))
	}
	return nil
}
