package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const rulesetName = "jauntbox: humans merge the default branch"

func TestRulesetBody(t *testing.T) {
	got, err := json.Marshal(RulesetBody([]string{"release/*"}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"bypass_actors":[{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"}],` +
		`"conditions":{"ref_name":{"exclude":[],"include":["~DEFAULT_BRANCH","refs/heads/release/*"]}},` +
		`"enforcement":"active","name":"` + rulesetName + `",` +
		`"rules":[{"type":"deletion"},{"type":"non_fast_forward"},{"type":"update"}],"target":"branch"}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

type call struct{ method, path, auth, body string }

func adminServer(t *testing.T, handler func(r *http.Request, body string) (int, string)) (*httptest.Server, *[]call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)})
		mu.Unlock()
		code, out := handler(r, string(b))
		w.WriteHeader(code)
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestLogin(t *testing.T) {
	srv, calls := adminServer(t, func(r *http.Request, _ string) (int, string) { return 200, `{"login":"tom"}` })
	got, err := Admin{Token: "adm", APIURL: srv.URL}.Login(context.Background())
	if err != nil || got != "tom" {
		t.Fatalf("got %q, %v", got, err)
	}
	if (*calls)[0].auth != "Bearer adm" || (*calls)[0].path != "/user" {
		t.Fatalf("call %+v", (*calls)[0])
	}
}

func TestEnsureRulesetCreates(t *testing.T) {
	srv, calls := adminServer(t, func(r *http.Request, _ string) (int, string) {
		if r.Method == "GET" {
			return 200, `[]`
		}
		return 201, `{"id":1}`
	})
	created, err := Admin{Token: "adm", APIURL: srv.URL}.EnsureRuleset(context.Background(), "o", "r", []string{"release/*"})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	c := *calls
	if len(c) != 2 || c[1].method != "POST" || c[1].path != "/repos/o/r/rulesets" {
		t.Fatalf("calls %+v", c)
	}
	want, _ := json.Marshal(RulesetBody([]string{"release/*"}))
	if c[1].body != string(want) {
		t.Fatalf("body %s", c[1].body)
	}
}

func TestEnsureRulesetUpdatesInPlace(t *testing.T) {
	srv, calls := adminServer(t, func(r *http.Request, _ string) (int, string) {
		if r.Method == "GET" {
			return 200, `[{"id":3,"name":"other"},{"id":9,"name":"` + rulesetName + `"}]`
		}
		return 200, `{"id":9}`
	})
	created, err := Admin{Token: "adm", APIURL: srv.URL}.EnsureRuleset(context.Background(), "o", "r", nil)
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	c := *calls
	if len(c) != 2 || c[1].method != "PUT" || c[1].path != "/repos/o/r/rulesets/9" {
		t.Fatalf("calls %+v", c)
	}
}

func rulesetJSON(t *testing.T, mutate func(m map[string]any)) string {
	t.Helper()
	b, _ := json.Marshal(RulesetBody([]string{"release/*"}))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = 9
	if mutate != nil {
		mutate(m)
	}
	out, _ := json.Marshal(m)
	return string(out)
}

func verifyWith(t *testing.T, list, get string) error {
	t.Helper()
	srv, _ := adminServer(t, func(r *http.Request, _ string) (int, string) {
		if r.URL.Path == "/repos/o/r/rulesets" {
			return 200, list
		}
		return 200, get
	})
	return Admin{Token: "adm", APIURL: srv.URL}.VerifyRuleset(context.Background(), "o", "r", []string{"release/*"})
}

func TestVerifyRulesetReportsDrift(t *testing.T) {
	list := `[{"id":9,"name":"` + rulesetName + `"}]`
	if err := verifyWith(t, list, rulesetJSON(t, nil)); err != nil {
		t.Fatalf("clean ruleset: %v", err)
	}
	err := verifyWith(t, list, rulesetJSON(t, func(m map[string]any) { m["enforcement"] = "evaluate" }))
	if err == nil || !strings.Contains(err.Error(), "enforcement") {
		t.Fatalf("enforcement: %v", err)
	}
	err = verifyWith(t, list, rulesetJSON(t, func(m map[string]any) {
		m["bypass_actors"] = append(m["bypass_actors"].([]any), map[string]any{"actor_id": 1, "actor_type": "Integration", "bypass_mode": "always"})
	}))
	if err == nil || !strings.Contains(err.Error(), "bypass") {
		t.Fatalf("bypass: %v", err)
	}
	err = verifyWith(t, list, rulesetJSON(t, func(m map[string]any) {
		m["conditions"].(map[string]any)["ref_name"].(map[string]any)["exclude"] = []any{"~DEFAULT_BRANCH"}
	}))
	if err == nil || !strings.Contains(err.Error(), "exclude") {
		t.Fatalf("exclude: %v", err)
	}
	err = verifyWith(t, list, rulesetJSON(t, func(m map[string]any) {
		m["rules"] = []any{map[string]any{"type": "deletion"}}
	}))
	if err == nil || !strings.Contains(err.Error(), "rules") {
		t.Fatalf("rules: %v", err)
	}
	err = verifyWith(t, `[]`, "")
	if err == nil || !strings.Contains(err.Error(), "jauntbox setup github") {
		t.Fatalf("missing: %v", err)
	}
}

func TestConvertManifest(t *testing.T) {
	srv, calls := adminServer(t, func(r *http.Request, _ string) (int, string) {
		return 201, `{"id":42,"slug":"jauntbox-tom","owner":{"login":"WA-Systems-EU","type":"Organization"},"pem":"PEMDATA","client_secret":"x"}`
	})
	meta, pem, err := Admin{Token: "adm", APIURL: srv.URL}.ConvertManifest(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if meta.AppID != 42 || meta.Slug != "jauntbox-tom" || meta.Owner != "WA-Systems-EU" || !meta.OwnerIsOrg || string(pem) != "PEMDATA" {
		t.Fatalf("meta %+v pem %q", meta, pem)
	}
	c := (*calls)[0]
	if c.method != "POST" || c.path != "/app-manifests/abc/conversions" || c.auth != "" {
		t.Fatalf("call %+v", c)
	}
}

func TestAdminErrorsOmitToken(t *testing.T) {
	srv, _ := adminServer(t, func(r *http.Request, _ string) (int, string) { return 401, `{"message":"Bad credentials"}` })
	_, err := Admin{Token: "sekrit-token", APIURL: srv.URL}.Login(context.Background())
	if err == nil || strings.Contains(err.Error(), "sekrit-token") {
		t.Fatalf("err %v", err)
	}
}

func TestOrgs(t *testing.T) {
	srv, calls := adminServer(t, func(r *http.Request, _ string) (int, string) {
		return 200, `[{"login":"WA-Systems-EU"},{"login":"other"}]`
	})
	got, err := Admin{Token: "adm", APIURL: srv.URL}.Orgs(context.Background())
	if err != nil || len(got) != 2 || got[0] != "WA-Systems-EU" || got[1] != "other" {
		t.Fatalf("got %v, %v", got, err)
	}
	if c := (*calls)[0]; c.auth != "Bearer adm" || c.path != "/user/orgs" {
		t.Fatalf("call %+v", c)
	}
}
