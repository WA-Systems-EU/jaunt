package client

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
)

// goneCheckWait is how long Down waits to see whether the box is already gone
// before reporting a refused root ssh login.
var goneCheckWait = 30 * time.Second

// Down deletes the box. Unless force is set it first asks the box to wrap up
// and delete itself, then waits up to WrapUpGrace+3m; if the box cannot be
// reached (or does not disappear in time) the box is deleted through the
// provider API. The home volume is never touched.
func Down(ctx context.Context, d Deps, force bool) error {
	box, err := d.Prov.FindBox(ctx, labels(d.Cfg.User, "box"))
	if err != nil {
		return fmt.Errorf("find box: %w", err)
	}
	if box == nil {
		fmt.Println("no box running")
		return nil
	}
	if err := d.Events.Emit(events.Event{Time: d.Now(), Kind: events.BoxDownRequested, Fields: map[string]string{
		"user": d.Cfg.User, "force": fmt.Sprint(force), "id": fmt.Sprint(box.ID),
	}}); err != nil {
		return fmt.Errorf("record box.down_requested: %w", err)
	}
	if !force {
		pol, err := d.Policy.Load(ctx)
		if err != nil {
			return fmt.Errorf("load policy: %w", err)
		}
		_, runErr := rootRun(ctx, d, "jauntctl", "stop")
		if ctx.Err() != nil { // Ctrl-C must never fall through to the force path
			return ctx.Err()
		}
		if errors.Is(runErr, ErrRootSSH) {
			if online, oerr := d.Mesh.DeviceOnline(ctx, hostname(d.Cfg.User)); oerr != nil || online {
				// The ssh session may have been cut by the box deleting itself: look briefly.
				gctx, cancel := context.WithTimeout(ctx, goneCheckWait)
				gone := d.Prov.WaitDeleted(gctx, box.ID)
				cancel()
				if gone == nil {
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("%w\n(`jauntbox down --force` deletes the box without the wrap-up)", runErr)
			}
		}
		if unreachable(runErr) {
			fmt.Printf("could not reach the box (%v); deleting it directly\n", runErr)
		} else {
			// The box got the request (or deleted itself under the ssh session): wait for it.
			wctx, cancel := context.WithTimeout(ctx, time.Duration(pol.Limits.WrapUpGrace)+3*time.Minute)
			defer cancel()
			if err := d.Prov.WaitDeleted(wctx, box.ID); err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("box did not delete itself in time; deleting it directly")
		}
	}
	if err := d.Prov.DeleteBox(ctx, box.ID); err != nil {
		return fmt.Errorf("delete box: %w", err)
	}
	return d.Prov.WaitDeleted(ctx, box.ID)
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
