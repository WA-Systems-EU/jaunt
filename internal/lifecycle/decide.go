// Package lifecycle holds the pure decision logic of the on-box supervisor.
package lifecycle

import (
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

type Action int

const (
	Keep Action = iota
	StartWrapUp
	Delete
)

type Verdict struct {
	Action Action
	Reason string
}

type Inputs struct {
	Now, UpAt, LastActive time.Time
	HumanIdle             time.Duration // smallest idle across sessions; very large if none
	AgentsRunning         int
	NightUntil            time.Time // zero = no night session
	StopRequested         bool
	WrapUpStartedAt       time.Time // zero = not wrapping up
	Limits                config.Limits
	Loc                   *time.Location
}

// Decide applies the lifecycle rules in their specified order.
func Decide(in Inputs) Verdict {
	l := in.Limits
	switch {
	case in.StopRequested:
		return stop(in, "stop-requested")
	case in.HumanIdle <= time.Duration(l.HumanActiveWindow):
		return Verdict{Keep, "human-active"}
	case !in.NightUntil.IsZero():
		if in.Now.Before(in.NightUntil) {
			if in.AgentsRunning > 0 {
				return Verdict{Keep, "night"}
			}
			return stop(in, "agents-done")
		}
		return stop(in, "night-deadline")
	}
	if dl, reason := Deadline(in.UpAt, l, in.Loc); !in.Now.Before(dl) {
		return stop(in, reason)
	}
	window := time.Duration(l.IdleDay)
	if InNightHours(in.Now, l.NightHours, in.Loc) {
		window = time.Duration(l.IdleNight)
	}
	if in.Now.Sub(in.LastActive) > window {
		return stop(in, "idle")
	}
	return Verdict{Keep, "ok"}
}

func stop(in Inputs, reason string) Verdict {
	if in.WrapUpStartedAt.IsZero() {
		if in.AgentsRunning == 0 {
			return Verdict{Delete, reason}
		}
		return Verdict{StartWrapUp, reason}
	}
	if in.AgentsRunning == 0 {
		return Verdict{Delete, "no-agents"}
	}
	if !in.Now.Before(in.WrapUpStartedAt.Add(time.Duration(in.Limits.WrapUpGrace))) {
		return Verdict{Delete, "wrapup-grace-elapsed"}
	}
	return Verdict{Keep, "wrapup-in-progress"}
}
