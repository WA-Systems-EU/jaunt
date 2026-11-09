// Package hetzner implements provider.Provider on top of hcloud-go.
package hetzner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/WA-Systems-EU/jaunt/internal/provider"
)

// DefaultMetadataURL is the Hetzner instance-id metadata endpoint.
const DefaultMetadataURL = "http://169.254.169.254/hetzner/v1/metadata/instance-id"

// waitDeletedPoll is the WaitDeleted poll interval (var so tests are fast).
var waitDeletedPoll = 2 * time.Second

// Hetzner is the Hetzner Cloud provider.
type Hetzner struct {
	c *hcloud.Client
}

var (
	_ provider.Provider   = (*Hetzner)(nil)
	_ provider.TypeLister = (*Hetzner)(nil)
)

// New builds a provider. Tests pass hcloud.WithEndpoint(srv.URL).
func New(token string, opts ...hcloud.ClientOption) *Hetzner {
	all := append([]hcloud.ClientOption{hcloud.WithToken(token)}, opts...)
	return &Hetzner{c: hcloud.NewClient(all...)}
}

func selector(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (h *Hetzner) EnsureSSHKey(ctx context.Context, name, publicKey string, labels map[string]string) (int64, error) {
	keys, err := h.c.SSHKey.AllWithOpts(ctx, hcloud.SSHKeyListOpts{Name: name})
	if err != nil {
		return 0, fmt.Errorf("list ssh keys: %w", err)
	}
	if len(keys) > 0 {
		return keys[0].ID, nil
	}
	k, _, err := h.c.SSHKey.Create(ctx, hcloud.SSHKeyCreateOpts{Name: name, PublicKey: publicKey, Labels: labels})
	if err != nil {
		return 0, fmt.Errorf("create ssh key %q: %w", name, err)
	}
	return k.ID, nil
}

// EnsureFirewall creates a firewall with no rules (all inbound denied; Tailscale
// needs only outbound/NAT traversal). It is applied to servers matching the label
// selector built from labels with role replaced by "box" (caller passes
// jauntbox=<user>, role=fw, giving jauntbox=<user>,role=box).
func (h *Hetzner) EnsureFirewall(ctx context.Context, name string, labels map[string]string) (int64, error) {
	if labels["jauntbox"] == "" {
		return 0, fmt.Errorf("firewall %q: labels must include jauntbox=<user>, otherwise the selector would match every user's box", name)
	}
	fws, err := h.c.Firewall.AllWithOpts(ctx, hcloud.FirewallListOpts{Name: name})
	if err != nil {
		return 0, fmt.Errorf("list firewalls: %w", err)
	}
	if len(fws) > 0 {
		return fws[0].ID, nil
	}
	sel := make(map[string]string, len(labels))
	for k, v := range labels {
		sel[k] = v
	}
	sel["role"] = "box"
	res, _, err := h.c.Firewall.Create(ctx, hcloud.FirewallCreateOpts{
		Name:   name,
		Labels: labels,
		ApplyTo: []hcloud.FirewallResource{{
			Type:          hcloud.FirewallResourceTypeLabelSelector,
			LabelSelector: &hcloud.FirewallResourceLabelSelector{Selector: selector(sel)},
		}},
	})
	if err != nil {
		return 0, fmt.Errorf("create firewall %q: %w", name, err)
	}
	if err := h.await(ctx, res.Actions...); err != nil {
		return 0, fmt.Errorf("firewall %q: %w", name, err)
	}
	return res.Firewall.ID, nil
}

// await waits for the given create actions so later calls (e.g. attaching the
// volume in CreateBox) do not hit a locked resource.
func (h *Hetzner) await(ctx context.Context, actions ...*hcloud.Action) error {
	var as []*hcloud.Action
	for _, a := range actions {
		if a != nil {
			as = append(as, a)
		}
	}
	if len(as) == 0 {
		return nil
	}
	if err := h.c.Action.WaitFor(ctx, as...); err != nil {
		return fmt.Errorf("wait for create action: %w", err)
	}
	return nil
}

func volumeOf(v *hcloud.Volume) provider.Volume {
	return provider.Volume{ID: v.ID, Name: v.Name, Device: fmt.Sprintf("/dev/disk/by-id/scsi-0HC_Volume_%d", v.ID)}
}

func (h *Hetzner) EnsureVolume(ctx context.Context, name string, sizeGB int, location string, labels map[string]string) (provider.Volume, error) {
	vols, err := h.c.Volume.AllWithOpts(ctx, hcloud.VolumeListOpts{Name: name})
	if err != nil {
		return provider.Volume{}, fmt.Errorf("list volumes: %w", err)
	}
	if len(vols) > 0 {
		v := vols[0]
		if v.Location != nil && v.Location.Name != location {
			return provider.Volume{}, fmt.Errorf("volume %q already exists in location %q but %q was requested; use that location (volumes cannot move)", name, v.Location.Name, location)
		}
		return volumeOf(v), nil
	}
	format := "ext4"
	automount := false
	res, _, err := h.c.Volume.Create(ctx, hcloud.VolumeCreateOpts{
		Name: name, Size: sizeGB, Location: &hcloud.Location{Name: location},
		Labels: labels, Format: &format, Automount: &automount,
	})
	if err != nil {
		return provider.Volume{}, fmt.Errorf("create volume %q: %w", name, err)
	}
	if err := h.await(ctx, append([]*hcloud.Action{res.Action}, res.NextActions...)...); err != nil {
		return provider.Volume{}, fmt.Errorf("volume %q: %w", name, err)
	}
	return volumeOf(res.Volume), nil
}

func boxOf(s *hcloud.Server) provider.Box {
	b := provider.Box{ID: s.ID, Name: s.Name, Status: string(s.Status), Created: s.Created}
	if s.ServerType != nil {
		b.ServerType = s.ServerType.Name
	}
	return b
}

func (h *Hetzner) FindBox(ctx context.Context, labels map[string]string) (*provider.Box, error) {
	if labels["jauntbox"] == "" || labels["role"] != "box" {
		return nil, fmt.Errorf("FindBox needs labels jauntbox=<user> and role=box, got %q; refusing to list every server in the project", selector(labels))
	}
	servers, err := h.c.Server.AllWithOpts(ctx, hcloud.ServerListOpts{ListOpts: hcloud.ListOpts{LabelSelector: selector(labels)}})
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	switch len(servers) {
	case 0:
		return nil, nil
	case 1:
		b := boxOf(servers[0])
		return &b, nil
	}
	return nil, fmt.Errorf("more than one server matches %s (%d found); delete the extras in the Hetzner console", selector(labels), len(servers))
}

func (h *Hetzner) CreateBox(ctx context.Context, spec provider.CreateBoxSpec) (provider.Box, error) {
	start, automount := true, false
	res, _, err := h.c.Server.Create(ctx, hcloud.ServerCreateOpts{
		Name:             spec.Name,
		ServerType:       &hcloud.ServerType{Name: spec.ServerType},
		Image:            &hcloud.Image{Name: spec.Image},
		Location:         &hcloud.Location{Name: spec.Location},
		SSHKeys:          []*hcloud.SSHKey{{ID: spec.SSHKeyID}},
		UserData:         spec.UserData,
		StartAfterCreate: &start,
		Labels:           spec.Labels,
		Automount:        &automount,
		Volumes:          []*hcloud.Volume{{ID: spec.VolumeID}},
		Firewalls:        []*hcloud.ServerCreateFirewall{{Firewall: hcloud.Firewall{ID: spec.FirewallID}}},
		PublicNet:        &hcloud.ServerCreatePublicNet{EnableIPv4: true, EnableIPv6: true},
	})
	if err != nil {
		return provider.Box{}, fmt.Errorf("create server %q: %w", spec.Name, err)
	}
	return boxOf(res.Server), nil
}

func (h *Hetzner) DeleteBox(ctx context.Context, id int64) error {
	if _, _, err := h.c.Server.DeleteWithResult(ctx, &hcloud.Server{ID: id}); err != nil {
		return fmt.Errorf("delete server %d: %w", id, err)
	}
	return nil
}

func (h *Hetzner) WaitDeleted(ctx context.Context, id int64) error {
	for {
		s, _, err := h.c.Server.GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("get server %d: %w", id, err)
		}
		if s == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for server %d to be deleted: %w", id, ctx.Err())
		case <-time.After(waitDeletedPoll):
		}
	}
}

func (h *Hetzner) ServerTypes(ctx context.Context) ([]provider.ServerType, error) {
	types, err := h.c.ServerType.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list server types: %w", err)
	}
	out := make([]provider.ServerType, 0, len(types))
	for _, t := range types {
		st := provider.ServerType{
			Name: t.Name, Cores: t.Cores, MemoryGB: float64(t.Memory),
			Deprecated: t.IsDeprecated(),
		}
		switch t.Architecture {
		case hcloud.ArchitectureX86:
			st.Arch = "amd64"
		case hcloud.ArchitectureARM:
			st.Arch = "arm64"
		default:
			st.Arch = string(t.Architecture)
		}
		for _, l := range t.Locations {
			if l.Location != nil && l.Available {
				st.Locations = append(st.Locations, l.Location.Name)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// SelfID returns this server's Hetzner ID from the metadata service.
// Pass "" for the default URL.
func SelfID(ctx context.Context, metadataURL string) (int64, error) {
	if metadataURL == "" {
		metadataURL = DefaultMetadataURL
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("query metadata service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("metadata service returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse instance id %q: %w", strings.TrimSpace(string(body)), err)
	}
	return id, nil
}
