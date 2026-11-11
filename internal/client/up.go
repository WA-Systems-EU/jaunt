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
	"github.com/WA-Systems-EU/jaunt/internal/ui"
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
	// Root logs in as root over Tailscale SSH. Every privileged step uses it;
	// the box user has no sudo for them and nothing falls back to Remote.
	Root   remote.Runner
	Events events.Sink
	// Secrets is the laptop-side secret store that unlock pushes to the box.
	Secrets secrets.Store
	Now     func() time.Time
	// Spawn starts a detached child of this binary with the given args.
	Spawn func(args ...string) error
	// HetznerToken is embedded in user-data so the box can delete itself.
	HetznerToken string
	// Steps reports progress; nil means silent (ui.Nop).
	Steps ui.Steps
	// AfterUp, when set, is called by Attach once Up has returned a box, so the
	// caller can plan the steps that follow (created: this run created it).
	AfterUp func(created bool)
	// BeforeAttach, when set, is called by Attach once the box is ready and
	// before anything else is printed (workspace warnings, reconnect notes), so
	// the caller can finish the step stream. created tells whether this run
	// created the box.
	BeforeAttach func(created bool)
}

// steps returns the progress reporter, never nil.
func (d Deps) steps() ui.Steps {
	if d.Steps == nil {
		return ui.Nop()
	}
	return d.Steps
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
	steps := d.steps()
	failed := func(st ui.Step, err error) error {
		st.Fail(err, "")
		return err
	}
	st := steps.Start("Policy and limits")
	pol, err := d.Policy.Load(ctx)
	if err != nil {
		return UpResult{}, failed(st, fmt.Errorf("load policy: %w", err))
	}
	prof, ok := pol.Profiles[profile]
	if !ok {
		return UpResult{}, failed(st, fmt.Errorf("unknown profile %q", profile))
	}
	st.Done()
	user := d.Cfg.User
	name := hostname(user)

	st = steps.Start("SSH key, firewall, volume")
	keyID, err := d.Prov.EnsureSSHKey(ctx, name, d.Cfg.SSHPublicKey, labels(user, "key"))
	if err != nil {
		return UpResult{}, failed(st, fmt.Errorf("ssh key: %w", err))
	}
	fwID, err := d.Prov.EnsureFirewall(ctx, name, labels(user, "fw"))
	if err != nil {
		return UpResult{}, failed(st, fmt.Errorf("firewall: %w", err))
	}
	ensureVol := func() (provider.Volume, error) {
		return d.Prov.EnsureVolume(ctx, name+"-home", d.Cfg.VolumeSizeGB, d.Cfg.Location, labels(user, "home"))
	}
	vol, err := ensureVol()
	if err != nil {
		// A concurrent `jauntbox up` may have created it first; look it up once more.
		var err2 error
		if vol, err2 = ensureVol(); err2 != nil {
			return UpResult{}, failed(st, fmt.Errorf("volume: %w (re-run `jauntbox up`)", err))
		}
	}
	st.Done()
	existing, err := d.Prov.FindBox(ctx, labels(user, "box"))
	if err != nil {
		return UpResult{}, failed(steps.Start("Box found"), fmt.Errorf("find box: %w", err))
	}
	found := func(b provider.Box) UpResult {
		fs := steps.Start("Box found")
		fs.Detail(fmt.Sprintf("%s (%s)", b.Name, b.Status))
		fs.Done()
		return UpResult{Box: b}
	}
	if existing != nil {
		return found(*existing), nil
	}

	st = steps.Start("Server created")
	// Hetzner can keep reporting the volume as attached for a few seconds after
	// the old server is gone (e.g. `down` then `up`); creating now would fail.
	wctx, cancel := context.WithTimeout(ctx, volumeDetachTimeout)
	err = d.Prov.WaitVolumeDetached(wctx, vol.ID)
	cancel()
	if err != nil {
		if again, ferr := d.Prov.FindBox(ctx, labels(user, "box")); ferr == nil && again != nil {
			// Lost a race: another `jauntbox up` created the box and holds the volume.
			st.Skip("another run created it")
			return found(*again), nil
		}
		switch {
		case ctx.Err() != nil:
			return UpResult{}, ctx.Err() // left open: the caller closes the steps as interrupted
		case errors.Is(err, provider.ErrVolumeInUse):
			return UpResult{}, failed(st, fmt.Errorf("home volume %s is attached to another server that is not a jauntbox box; detach it first: %w", name+"-home", err))
		case errors.Is(err, context.DeadlineExceeded):
			return UpResult{}, failed(st, fmt.Errorf("volume %s is still attached to another server (wait a minute, then re-run `jauntbox up`): %w", name+"-home", err))
		default:
			return UpResult{}, failed(st, fmt.Errorf("check volume attachment: %w", err))
		}
	}

	tag := "tag:" + name
	authKey, err := d.Mesh.CreateAuthKey(ctx, []string{tag}, time.Hour)
	if err != nil {
		return UpResult{}, failed(st, fmt.Errorf("tailscale auth key: %w", err))
	}
	userData, err := cloudinit.Render(cloudinit.Params{
		Box: boxinfo.BoxInfo{
			User: user, Hostname: name, Profile: profile, Timezone: d.Cfg.Timezone,
			UpAt: d.Now(), Limits: pol.Limits, Agent: d.Cfg.Agent,
		},
		TailscaleAuthKey: authKey,
		HetznerToken:     d.HetznerToken,
		VolumeDevice:     vol.Device,
		Tag:              tag,
	})
	if err != nil {
		return UpResult{}, failed(st, err)
	}
	box, err := d.Prov.CreateBox(ctx, provider.CreateBoxSpec{
		Name: name, ServerType: prof.ServerType, Image: d.Cfg.Image, Location: d.Cfg.Location,
		UserData: userData, Labels: labels(user, "box"),
		VolumeID: vol.ID, SSHKeyID: keyID, FirewallID: fwID,
	})
	if err != nil {
		if again, ferr := d.Prov.FindBox(ctx, labels(user, "box")); ferr == nil && again != nil {
			// Lost a race: another `jauntbox up` already created it.
			st.Skip("another run created it")
			return found(*again), nil
		}
		return UpResult{}, failed(st, fmt.Errorf("create box: %w (if another `jauntbox up` is running, re-run `jauntbox up`)", err))
	}
	st.Detail(fmt.Sprintf("#%d", box.ID))
	emitErr := d.Events.Emit(events.Event{Time: d.Now(), Kind: events.BoxCreated, Fields: map[string]string{
		"user": user, "profile": profile, "server_type": prof.ServerType, "id": fmt.Sprint(box.ID),
	}})
	if emitErr != nil {
		st.Warn("could not record the box.created event: " + emitErr.Error())
	}
	st.Done()
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
