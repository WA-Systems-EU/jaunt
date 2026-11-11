package agentrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
)

// StopTimeout is how long `docker stop` waits before killing the container.
const StopTimeout = 10 * time.Second

// FallbackTimeout bounds the in-container mechanical wrap-up.
var FallbackTimeout = 3 * time.Minute

// RequestWrapUp raises the wrap-up flag of an active run: it writes the
// run's tmpfs `wrapup` file (0644, content = reason), which the in-container
// hooks turn into an instruction, and records WrapUpAt. A run already asked
// to wrap up keeps its first time and reason (the grace runs from it).
func RequestWrapUp(p Paths, id, reason string, now time.Time) error {
	r, err := p.Load(id)
	if err != nil {
		return err
	}
	if !r.FinishedAt.IsZero() {
		return fmt.Errorf("run %s has already finished", id)
	}
	if r.WrapUpAt.IsZero() {
		r.WrapUpAt, r.WrapUpReason = now, reason
	}
	if err := writeFlag(filepath.Join(p.RunsTmp, id), "wrapup", []byte(r.WrapUpReason), 0o644); err != nil {
		return err
	}
	return p.Save(r)
}

// writeFlag replaces dir/name atomically (temp file + rename) without
// following links. dir is a root-owned run dir.
func writeFlag(dir, name string, data []byte, mode os.FileMode) error {
	tmp := filepath.Join(dir, "."+name+".tmp")
	_ = os.Remove(tmp)
	if err := writeNew(tmp, data, mode, -1, -1); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// TeardownTimeout bounds the container teardown of one Finish. It runs on a
// context detached from the caller's deadline, so a slow fallback that used
// up that deadline cannot make the teardown fail.
const TeardownTimeout = 30 * time.Second

// Finish ends a run: while its container is running, the mechanical
// fallback (`jaunt-wrapup-fallback <reason>`) runs inside it first (it
// pushes, so it must run before tokens are revoked); an exited or missing
// container gets none. Then the container and its network are removed, the
// run's host-port rules are revoked (errors ignored; a nil net skips it,
// and it happens whether or not the container existed), the
// tmpfs run dir is deleted, FinishedAt is recorded and agent.stopped is
// emitted, all stamped with now. The fallback gets FallbackTimeout; on
// timeout it reads "failed: timeout" and the teardown goes on. The exit code
// is recorded only when the container exited (a running one is inspected
// again after the stop). Finishing a finished run does nothing.
func Finish(ctx context.Context, d Docker, p Paths, net Runner, id string, ev events.Sink, now time.Time) error {
	// ctx bounds the fallback only (min(FallbackTimeout, what is left of
	// it)); everything else runs detached with its own timeout.
	fallbackCtx := ctx
	ctx, cancelTeardown := context.WithTimeout(context.WithoutCancel(ctx), TeardownTimeout)
	defer cancelTeardown()
	r, err := p.Load(id)
	if err != nil {
		return err
	}
	if !r.FinishedAt.IsZero() {
		return nil
	}
	name := ContainerName(id)
	st, err := d.Inspect(ctx, name)
	if err != nil {
		return err
	}
	reason := r.WrapUpReason
	if reason == "" {
		reason = "stop-requested"
	}
	fields := map[string]string{"run": r.ID, "repo": r.Repo, "reason": reason}

	if st.Running {
		fctx, cancel := context.WithTimeout(fallbackCtx, FallbackTimeout)
		out, err := d.Exec(fctx, name, "", "jaunt-wrapup-fallback", reason)
		timedOut := errors.Is(fctx.Err(), context.DeadlineExceeded)
		cancel()
		msg := strings.TrimSpace(string(out))
		switch {
		case timedOut:
			msg = "failed: timeout"
		case err != nil && msg == "":
			msg = "failed: " + err.Error()
		}
		fields["fallback"] = msg
	}

	var errs []error
	exitKnown := false
	if st.Exists {
		// rm -f below kills it if the graceful stop fails.
		_ = d.Stop(ctx, name, StopTimeout)
		if st.Running {
			// The exit code of a container we stopped is the stop's.
			if st2, err := d.Inspect(ctx, name); err == nil && st2.Exists && !st2.Running {
				r.ExitCode, exitKnown = st2.ExitCode, true
			}
		} else {
			r.ExitCode, exitKnown = st.ExitCode, true
		}
		if err := d.Remove(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("remove container %s: %w", name, err))
		}
	}
	// The network outlives a container that is already gone; a missing
	// network is only an error while we also had a container to remove.
	if err := d.NetworkRemove(ctx, name); err != nil && st.Exists {
		errs = append(errs, fmt.Errorf("remove network %s: %w", name, err))
	}
	if net != nil {
		RevokeRunPorts(ctx, net, id, r.RunPorts)
	}
	if err := os.RemoveAll(filepath.Join(p.RunsTmp, id)); err != nil {
		errs = append(errs, err)
	}
	if err := os.Remove(filepath.Join(p.RunsTmp, id+".env")); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	r.FinishedAt = now
	if err := p.Save(r); err != nil {
		return err
	}
	if exitKnown {
		fields["exit_code"] = strconv.Itoa(r.ExitCode)
	}
	if ev != nil {
		_ = ev.Emit(events.Event{Time: now.UTC(), Kind: events.AgentStopped, Fields: fields})
	}
	return nil
}
