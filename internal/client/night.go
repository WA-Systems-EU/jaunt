package client

import (
	"context"
	"fmt"
	"time"
)

// nextOccurrence returns the first instant strictly after now whose wall clock
// in loc reads hh:mm. time.Date keeps this correct across DST changes.
func nextOccurrence(now time.Time, hhmm string, loc *time.Location) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, fmt.Errorf("--until %q must be HH:MM (24-hour, e.g. 07:00)", hhmm)
	}
	n := now.In(loc)
	at := time.Date(n.Year(), n.Month(), n.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	if !at.After(now) {
		at = time.Date(n.Year(), n.Month(), n.Day()+1, t.Hour(), t.Minute(), 0, 0, loc)
	}
	return at, nil
}

// Night authorises a night session on the box until the next HH:MM in the
// configured timezone.
func Night(ctx context.Context, d Deps, until string) error {
	loc, err := time.LoadLocation(d.Cfg.Timezone)
	if err != nil {
		return fmt.Errorf("timezone %q: %w", d.Cfg.Timezone, err)
	}
	at, err := nextOccurrence(d.Now(), until, loc)
	if err != nil {
		return err
	}
	if _, err := d.Remote.Run(ctx, hostname(d.Cfg.User), "sudo", "jauntctl", "night-set", "--until", at.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("night-set on the box: %w", err)
	}
	fmt.Printf("night session authorised until %s\n", at.Format(time.RFC3339))
	return nil
}
