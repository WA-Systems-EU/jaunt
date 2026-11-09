// Package client implements the laptop-side jauntbox commands.
package client

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/cloudinit"
	"github.com/WA-Systems-EU/jaunt/internal/config"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/mesh"
	"github.com/WA-Systems-EU/jaunt/internal/policy"
	"github.com/WA-Systems-EU/jaunt/internal/provider"
	"github.com/WA-Systems-EU/jaunt/internal/remote"
)

// Deps are the collaborators of the client commands.
type Deps struct {
	Cfg    config.Config
	Policy policy.Source
	Prov   provider.Provider
	Mesh   mesh.Mesh
	Remote remote.Runner
	Events events.Sink
	Now    func() time.Time
	// Spawn starts a detached child of this binary with the given args.
	Spawn func(args ...string) error
	// HetznerToken is embedded in user-data so the box can delete itself.
	HetznerToken string
}

// UpResult reports what Up did.
type UpResult struct {
	Box     provider.Box
	Created bool
}

func hostname(user string) string { return "jauntbox-" + user }

func labels(user, role string) map[string]string {
	return map[string]string{"jauntbox": user, "role": role}
}

// Up idempotently provisions the box. If a box already exists it returns it
// with Created=false and no further side effects.
func Up(ctx context.Context, d Deps, profile string) (UpResult, error) {
	pol, err := d.Policy.Load(ctx)
	if err != nil {
		return UpResult{}, fmt.Errorf("load policy: %w", err)
	}
	prof, ok := pol.Profiles[profile]
	if !ok {
		return UpResult{}, fmt.Errorf("unknown profile %q", profile)
	}
	user := d.Cfg.User
	name := hostname(user)

	keyID, err := d.Prov.EnsureSSHKey(ctx, name, d.Cfg.SSHPublicKey, labels(user, "key"))
	if err != nil {
		return UpResult{}, fmt.Errorf("ssh key: %w", err)
	}
	fwID, err := d.Prov.EnsureFirewall(ctx, name, labels(user, "fw"))
	if err != nil {
		return UpResult{}, fmt.Errorf("firewall: %w", err)
	}
	vol, err := d.Prov.EnsureVolume(ctx, name+"-home", d.Cfg.VolumeSizeGB, d.Cfg.Location, labels(user, "home"))
	if err != nil {
		return UpResult{}, fmt.Errorf("volume: %w", err)
	}
	existing, err := d.Prov.FindBox(ctx, labels(user, "box"))
	if err != nil {
		return UpResult{}, fmt.Errorf("find box: %w", err)
	}
	if existing != nil {
		return UpResult{Box: *existing}, nil
	}

	tag := "tag:" + name
	authKey, err := d.Mesh.CreateAuthKey(ctx, []string{tag}, time.Hour)
	if err != nil {
		return UpResult{}, fmt.Errorf("tailscale auth key: %w", err)
	}
	userData, err := cloudinit.Render(cloudinit.Params{
		Box: boxinfo.BoxInfo{
			User: user, Hostname: name, Profile: profile, Timezone: d.Cfg.Timezone,
			UpAt: d.Now(), Limits: pol.Limits,
		},
		TailscaleAuthKey: authKey,
		HetznerToken:     d.HetznerToken,
		VolumeDevice:     vol.Device,
		Tag:              tag,
	})
	if err != nil {
		return UpResult{}, err
	}
	box, err := d.Prov.CreateBox(ctx, provider.CreateBoxSpec{
		Name: name, ServerType: prof.ServerType, Image: d.Cfg.Image, Location: d.Cfg.Location,
		UserData: userData, Labels: labels(user, "box"),
		VolumeID: vol.ID, SSHKeyID: keyID, FirewallID: fwID,
	})
	if err != nil {
		return UpResult{}, fmt.Errorf("create box: %w", err)
	}
	_ = d.Events.Emit(events.Event{Time: d.Now(), Kind: events.BoxCreated, Fields: map[string]string{
		"user": user, "profile": profile, "server_type": prof.ServerType, "id": fmt.Sprint(box.ID),
	}})
	if err := d.Spawn("internal-push"); err != nil {
		return UpResult{Box: box, Created: true}, fmt.Errorf("box created but starting the binary push failed (run `jauntbox internal-push`): %w", err)
	}
	return UpResult{Box: box, Created: true}, nil
}

// BoxBinaryPath returns the linux/amd64 jauntbox binary to install on the box.
func BoxBinaryPath(cfg config.Config) (string, error) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		return osExecutable()
	}
	if cfg.BoxBinary == "" {
		return "", fmt.Errorf("box_binary must be set on non-linux/amd64 clients")
	}
	return cfg.BoxBinary, nil
}

// WithoutSpawn returns a copy of d whose Spawn does nothing, for callers that
// run PushBinary in the foreground.
func WithoutSpawn(d Deps) Deps {
	d.Spawn = func(...string) error { return nil }
	return d
}
