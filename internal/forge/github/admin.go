package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// RulesetName names the branch ruleset jauntbox manages on each repository.
const RulesetName = "jauntbox: humans merge the default branch"

// Admin performs the few operations that need the human's admin credential.
// It is used only by `jauntbox setup github` on the laptop.
type Admin struct {
	Token  string
	APIURL string // "" means https://api.github.com
}

func (a Admin) base() string {
	if a.APIURL == "" {
		return defaultAPIURL
	}
	return strings.TrimRight(a.APIURL, "/")
}

func (a Admin) do(ctx context.Context, method, path string, body, out any) error {
	hc := &http.Client{Timeout: 10 * time.Second}
	return doRequest(ctx, hc, a.base(), method, path, "Bearer "+a.Token, body, out)
}

// Login returns the login of the user the admin token belongs to.
func (a Admin) Login(ctx context.Context) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if err := a.do(ctx, "GET", "/user", nil, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

// ConvertManifest exchanges the code from the App manifest flow for the App
// metadata and its private key. GitHub needs no authentication for this call.
func (a Admin) ConvertManifest(ctx context.Context, code string) (AppMeta, []byte, error) {
	var out struct {
		ID    int64  `json:"id"`
		Slug  string `json:"slug"`
		PEM   string `json:"pem"`
		Owner struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	path := "/app-manifests/" + url.PathEscape(code) + "/conversions"
	if err := doRequest(ctx, hc, a.base(), "POST", path, "", nil, &out); err != nil {
		return AppMeta{}, nil, err
	}
	if out.ID == 0 || out.PEM == "" {
		return AppMeta{}, nil, fmt.Errorf("manifest conversion returned no app id or key")
	}
	meta := AppMeta{AppID: out.ID, Slug: out.Slug, Owner: out.Owner.Login, OwnerIsOrg: out.Owner.Type == "Organization"}
	return meta, []byte(out.PEM), nil
}

// RulesetBody is the ruleset jauntbox keeps on each repository: the default
// branch and the protected patterns cannot be deleted, force-pushed or updated
// directly; only repository admins (the human) may bypass.
func RulesetBody(patterns []string) map[string]any {
	include := []string{"~DEFAULT_BRANCH"}
	for _, p := range patterns {
		include = append(include, "refs/heads/"+p)
	}
	return map[string]any{
		"name":        RulesetName,
		"target":      "branch",
		"enforcement": "active",
		"conditions":  map[string]any{"ref_name": map[string]any{"include": include, "exclude": []string{}}},
		"rules":       []map[string]any{{"type": "deletion"}, {"type": "non_fast_forward"}, {"type": "update"}},
		"bypass_actors": []map[string]any{
			{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"},
		},
	}
}

func (a Admin) findRuleset(ctx context.Context, owner, repo string) (int64, bool, error) {
	var list []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := a.do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/rulesets", owner, repo), nil, &list); err != nil {
		return 0, false, err
	}
	for _, r := range list {
		if r.Name == RulesetName {
			return r.ID, true, nil
		}
	}
	return 0, false, nil
}

// EnsureRuleset creates the ruleset, or updates it in place when it exists.
func (a Admin) EnsureRuleset(ctx context.Context, owner, repo string, patterns []string) (bool, error) {
	id, found, err := a.findRuleset(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	base := fmt.Sprintf("/repos/%s/%s/rulesets", owner, repo)
	if found {
		return false, a.do(ctx, "PUT", fmt.Sprintf("%s/%d", base, id), RulesetBody(patterns), nil)
	}
	return true, a.do(ctx, "POST", base, RulesetBody(patterns), nil)
}

func sortedEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// VerifyRuleset checks the live ruleset still matches RulesetBody(patterns).
func (a Admin) VerifyRuleset(ctx context.Context, owner, repo string, patterns []string) error {
	id, found, err := a.findRuleset(ctx, owner, repo)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s/%s has no %q ruleset; run `jauntbox setup github`", owner, repo, RulesetName)
	}
	var got struct {
		Enforcement string `json:"enforcement"`
		Target      string `json:"target"`
		Conditions  struct {
			RefName struct {
				Include []string `json:"include"`
			} `json:"ref_name"`
		} `json:"conditions"`
		Rules []struct {
			Type string `json:"type"`
		} `json:"rules"`
		Bypass []struct {
			ActorID    int64  `json:"actor_id"`
			ActorType  string `json:"actor_type"`
			BypassMode string `json:"bypass_mode"`
		} `json:"bypass_actors"`
	}
	if err := a.do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/rulesets/%d", owner, repo, id), nil, &got); err != nil {
		return err
	}
	var drift []string
	if got.Enforcement != "active" {
		drift = append(drift, fmt.Sprintf("enforcement is %q, want \"active\"", got.Enforcement))
	}
	if got.Target != "branch" {
		drift = append(drift, fmt.Sprintf("target is %q, want \"branch\"", got.Target))
	}
	want := RulesetBody(patterns)["conditions"].(map[string]any)["ref_name"].(map[string]any)["include"].([]string)
	if !sortedEq(got.Conditions.RefName.Include, want) {
		drift = append(drift, fmt.Sprintf("ref include is %v, want %v", got.Conditions.RefName.Include, want))
	}
	var types []string
	for _, r := range got.Rules {
		types = append(types, r.Type)
	}
	if !sortedEq(types, []string{"deletion", "non_fast_forward", "update"}) {
		drift = append(drift, fmt.Sprintf("rules are %v, want deletion, non_fast_forward, update", types))
	}
	if len(got.Bypass) != 1 || got.Bypass[0].ActorID != 5 || got.Bypass[0].ActorType != "RepositoryRole" || got.Bypass[0].BypassMode != "always" {
		drift = append(drift, "bypass actors differ: only RepositoryRole 5 (repository admin) with mode always may bypass")
	}
	if len(drift) > 0 {
		return fmt.Errorf("%s/%s ruleset drifted: %s; run `jauntbox setup github` to repair", owner, repo, strings.Join(drift, "; "))
	}
	return nil
}
