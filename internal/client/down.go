package client

import (
	"context"
	"fmt"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
)

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
		if _, err := d.Remote.Run(ctx, hostname(d.Cfg.User), "sudo", "jauntbox", "stop"); err == nil {
			wctx, cancel := context.WithTimeout(ctx, time.Duration(pol.Limits.WrapUpGrace)+3*time.Minute)
			defer cancel()
			if err := d.Prov.WaitDeleted(wctx, box.ID); err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Println("box did not delete itself in time; deleting it directly")
		} else {
			fmt.Printf("could not reach the box (%v); deleting it directly\n", err)
		}
	}
	if err := d.Prov.DeleteBox(ctx, box.ID); err != nil {
		return fmt.Errorf("delete box: %w", err)
	}
	return d.Prov.WaitDeleted(ctx, box.ID)
}
