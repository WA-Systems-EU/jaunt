// Package client implements the laptop-side jauntbox commands.
package client

import (
	"context"
	"errors"
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
	"github.com/WA-Systems-EU/jaunt/internal/secrets"
)

// volumeDetachTimeout bounds how long Up waits for the home volume to be
// reported detached (var so tests are fast).
var volumeDetachTimeout = 2 * time.Minute

// Deps are the collaborators of the client commands.
type Deps struct {
	Cfg    config.Config
	Policy policy.Source
	Prov   provider.Provider
	Mesh   mesh.Mesh
	Remote remote.Runner
	Events events.Sink
	// Secrets is the laptop-side secret store that unlock pushes to the box.
	Secrets secrets.Store
	Now     func() time.Time
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
	ensureVol := func() (provider.Volume, error) {
		return d.Prov.EnsureVolume(ctx, name+"-home", d.Cfg.VolumeSizeGB, d.Cfg.Location, labels(user, "home"))
	}
	vol, err := ensureVol()
	if err != nil {
		// A concurrent `jauntbox up` may have created it first; look it up once more.
		var err2 error
		if vol, err2 = ensureVol(); err2 != nil {
			return UpResult{}, fmt.Errorf("volume: %w (re-run `jauntbox up`)", err)
		}
	}
	existing, err := d.Prov.FindBox(ctx, labels(user, "box"))
	if err != nil {
		return UpResult{}, fmt.Errorf("find box: %w", err)
	}
	if existing != nil {
		return UpResult{Box: *existing}, nil
	}

	// Hetzner can keep reporting the volume as attached for a few seconds after
	// the old server is gone (e.g. `down` then `up`); creating now would fail.
	wctx, cancel := context.WithTimeout(ctx, volumeDetachTimeout)
	err = d.Prov.WaitVolumeDetached(wctx, vol.ID)
	cancel()
	if err != nil {
		if again, ferr := d.Prov.FindBox(ctx, labels(user, "box")); ferr == nil && again != nil {
			// Lost a race: another `jauntbox up` created the box and holds the volume.
			return UpResult{Box: *again}, nil
		}
		switch {
		case ctx.Err() != nil:
			return UpResult{}, ctx.Err()
		case errors.Is(err, provider.ErrVolumeInUse):
			return UpResult{}, fmt.Errorf("home volume %s is attached to another server that is not a jauntbox box; detach it first: %w", name+"-home", err)
		case errors.Is(err, context.DeadlineExceeded):
			return UpResult{}, fmt.Errorf("volume %s is still attached to another server (wait a minute, then re-run `jauntbox up`): %w", name+"-home", err)
		default:
			return UpResult{}, fmt.Errorf("check volume attachment: %w", err)
		}
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
		if again, ferr := d.Prov.FindBox(ctx, labels(user, "box")); ferr == nil && again != nil {
			// Lost a race: another `jauntbox up` already created it.
			return UpResult{Box: *again}, nil
		}
		return UpResult{}, fmt.Errorf("create box: %w (if another `jauntbox up` is running, re-run `jauntbox up`)", err)
	}
	emitErr := d.Events.Emit(events.Event{Time: d.Now(), Kind: events.BoxCreated, Fields: map[string]string{
		"user": user, "profile": profile, "server_type": prof.ServerType, "id": fmt.Sprint(box.ID),
	}})
	res := UpResult{Box: box, Created: true}
	if err := d.Spawn("internal-push"); err != nil {
		return res, fmt.Errorf("box created but starting the binary push failed (run `jauntbox internal-push`): %w", err)
	}
	if emitErr != nil {
		return res, fmt.Errorf("box created, but recording the box.created event failed: %w", emitErr)
	}
	return res, nil
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
