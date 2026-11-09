package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.tailscale.com"

// Tailscale is a Mesh backed by the Tailscale REST API (stdlib only).
type Tailscale struct {
	apiKey  string
	baseURL string
	hc      *http.Client
}

// NewTailscale builds a client. Empty baseURL uses the public API; nil hc uses
// a client with a 30s timeout.
func NewTailscale(apiKey, baseURL string, hc *http.Client) *Tailscale {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Tailscale{apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), hc: hc}
}

func (t *Tailscale) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.hc.Do(req)
	if err != nil {
		return fmt.Errorf("tailscale %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		excerpt := strings.TrimSpace(string(data))
		if len(excerpt) > 200 {
			excerpt = excerpt[:200] + "..."
		}
		msg := fmt.Sprintf("tailscale %s %s: HTTP %d: %s", method, path, resp.StatusCode, excerpt)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			msg += " (check TAILSCALE_API_KEY is valid and its key scopes allow auth keys and reading devices)"
		}
		return fmt.Errorf("%s", msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("tailscale %s %s: decode response: %w", method, path, err)
	}
	return nil
}

// CreateAuthKey mints a single-use, ephemeral, preauthorized key for tags.
func (t *Tailscale) CreateAuthKey(ctx context.Context, tags []string, expiry time.Duration) (string, error) {
	req := map[string]any{
		"capabilities": map[string]any{"devices": map[string]any{"create": map[string]any{
			"reusable": false, "ephemeral": true, "preauthorized": true, "tags": tags,
		}}},
		"expirySeconds": int(expiry.Seconds()),
	}
	var resp struct {
		Key string `json:"key"`
	}
	if err := t.do(ctx, http.MethodPost, "/api/v2/tailnet/-/keys", req, &resp); err != nil {
		return "", err
	}
	if resp.Key == "" {
		return "", fmt.Errorf("tailscale create auth key: response had no key")
	}
	return resp.Key, nil
}

// DeviceOnline reports whether a device with the given hostname is connected.
// It uses connectedToControl when present, else lastSeen within 2 minutes.
func (t *Tailscale) DeviceOnline(ctx context.Context, hostname string) (bool, error) {
	var resp struct {
		Devices []struct {
			Hostname           string `json:"hostname"`
			Name               string `json:"name"`
			ConnectedToControl *bool  `json:"connectedToControl"`
			LastSeen           string `json:"lastSeen"`
		} `json:"devices"`
	}
	if err := t.do(ctx, http.MethodGet, "/api/v2/tailnet/-/devices", nil, &resp); err != nil {
		return false, err
	}
	h := strings.ToLower(hostname)
	for _, d := range resp.Devices {
		if strings.ToLower(d.Hostname) != h && !strings.HasPrefix(strings.ToLower(d.Name), h+".") {
			continue
		}
		if d.ConnectedToControl != nil {
			return *d.ConnectedToControl, nil
		}
		ls, err := time.Parse(time.RFC3339, d.LastSeen)
		if err != nil {
			return false, nil
		}
		return time.Since(ls) < 2*time.Minute, nil
	}
	return false, nil
}

// ACLSnippet returns a HuJSON fragment to merge into the tailnet policy file.
func ACLSnippet(user string) string {
	return fmt.Sprintf(`{
  // Merge into your tailnet policy file.
  "tagOwners": {
    "tag:jauntbox-%[1]s": ["autogroup:admin"]
  },
  "ssh": [
    {
      // Let the owner SSH to their own box as their own user.
      "action": "accept",
      "src":    ["autogroup:member"],
      "dst":    ["tag:jauntbox-%[1]s"],
      "users":  ["%[1]s"]
    }
  ]
  // No grant or acl from tag:jauntbox-%[1]s to anything else exists,
  // so the box cannot reach your servers (box -> servers is blocked).
}
`, user)
}
