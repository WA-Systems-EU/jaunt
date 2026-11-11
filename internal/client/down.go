package client

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/supervisor"
)

// goneCheckWait is how long Down waits to see whether the box is already gone
// before reporting a refused root ssh login.
var goneCheckWait = 30 * time.Second

// downMargin covers the sync, the self-delete and Hetzner reporting the box
// gone after the supervisor's final pass.
const downMargin = 2 * time.Minute

// downWait is how long Down waits for the box to wrap up its agents and
// delete itself: the wrap-up grace, up to one timer interval until the pass
// that sees it elapsed, that pass (agent finishes with their fallback
// pushes, token revoke; systemd caps it at PassTimeout) and a margin.
func downWait(grace time.Duration) time.Duration {
	return grace + supervisor.TimerInterval + supervisor.PassTimeout + downMargin
}

// Down deletes the box. Unless force is set it first asks the box to wrap up
// and delete itself, then waits up to downWait(WrapUpGrace); if the box cannot be
// reached (or does not disappear in time) the box is deleted through the
// provider API. The home volume is never touched. With no box it ends the
// step stream as not ok ("no box running") and returns nil.
func Down(ctx context.Context, d Deps, force bool) error {
	found, err := down(ctx, d, force)
	if err == nil && !found {
		d.steps().End(false, "", "no box running")
	}
	return err
}

// down is Down without the "no box" ending; found is false when there was no box.
func down(ctx context.Context, d Deps, force bool) (found bool, _ error) {
	steps := d.steps()
	box, err := d.Prov.FindBox(ctx, labels(d.Cfg.User, "box"))
	if err != nil {
		return true, fmt.Errorf("find box: %w", err)
	}
	if box == nil {
		return false, nil
	}
	if err := d.Events.Emit(events.Event{Time: d.Now(), Kind: events.BoxDownRequested, Fields: map[string]string{
		"user": d.Cfg.User, "force": fmt.Sprint(force), "id": fmt.Sprint(box.ID),
	}}); err != nil {
		return true, fmt.Errorf("record box.down_requested: %w", err)
	}
	deleted := func() {
		st := steps.Start("Box deleted")
		st.Detail("volume kept")
		st.Done()
	}
	directLabel := "Delete the box"
	if !force {
		directLabel = "Delete the box directly"
		pol, err := d.Policy.Load(ctx)
		if err != nil {
			return true, fmt.Errorf("load policy: %w", err)
		}
		ask := steps.Start("Ask the box to wrap up")
		_, runErr := rootRun(ctx, d, "jauntctl", "stop")
		if ctx.Err() != nil { // Ctrl-C must never fall through to the force path
			return true, ctx.Err() // left open: the caller closes the steps as interrupted
		}
		if errors.Is(runErr, ErrRootSSH) {
			if online, oerr := d.Mesh.DeviceOnline(ctx, hostname(d.Cfg.User)); oerr != nil || online {
				// The ssh session may have been cut by the box deleting itself: look briefly.
				gctx, cancel := context.WithTimeout(ctx, goneCheckWait)
				gone := d.Prov.WaitDeleted(gctx, box.ID)
				cancel()
				if gone == nil {
					ask.Done()
					deleted()
					return true, nil
				}
				if ctx.Err() != nil {
					return true, ctx.Err()
				}
				err := fmt.Errorf("%w\n(`jauntbox down --force` deletes the box without the wrap-up)", runErr)
				ask.Fail(err, "")
				return true, err
			}
		}
		if unreachable(runErr) {
			ask.Warn(fmt.Sprintf("could not reach the box (%v)", runErr))
			ask.Done()
		} else {
			// The box got the request (or deleted itself under the ssh session): wait for it.
			ask.Done()
			wait := downWait(time.Duration(pol.Limits.WrapUpGrace))
			ws := steps.Start("Wait for the box to delete itself")
			ws.Detail(fmt.Sprintf("up to %d min", int((wait+time.Minute-1)/time.Minute)))
			wctx, cancel := context.WithTimeout(ctx, wait)
			defer cancel()
			if err := d.Prov.WaitDeleted(wctx, box.ID); err == nil {
				ws.Done()
				deleted()
				return true, nil
			}
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			ws.Warn("did not delete itself in time")
			ws.Done()
		}
	}
	del := steps.Start(directLabel)
	if err := d.Prov.DeleteBox(ctx, box.ID); err != nil {
		err = fmt.Errorf("delete box: %w", err)
		if ctx.Err() == nil { // cancelled: leave the step open, Close shows "interrupted"
			del.Fail(err, "")
		}
		return true, err
	}
	if err := d.Prov.WaitDeleted(ctx, box.ID); err != nil {
		if ctx.Err() == nil {
			del.Fail(err, "")
		}
		return true, err
	}
	del.Done()
	deleted()
	return true, nil
}

// unreachable reports whether err means ssh never reached the box: a failure to
// run ssh at all, or ssh's own exit status 255. Any other exit status comes from
// the remote command (or from the box going away mid-session).
func unreachable(err error) bool {
	if err == nil {
		return false
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode() == 255
	}
	return true
}
