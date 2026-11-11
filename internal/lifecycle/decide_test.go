package lifecycle

import (
	"testing"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

var paris = mustLoc("Europe/Paris")

func mustLoc(n string) *time.Location {
	l, err := time.LoadLocation(n)
	if err != nil {
		panic(err)
	}
	return l
}

func at(day, hh, mm int) time.Time { // 2026-06-<day> in Paris
	return time.Date(2026, 6, day, hh, mm, 0, 0, paris)
}

const none = 1000 * time.Hour // "no session": very large idle

func base() Inputs {
	return Inputs{Limits: config.Default().Limits, Loc: paris, HumanIdle: none}
}

func TestDecide(t *testing.T) {
	// Fields omitted by a case are chosen so they do not change the outcome.
	mk := func(f func(in *Inputs)) Inputs { in := base(); f(&in); return in }
	wrap := func(in *Inputs) { // case 5 setup
		in.UpAt, in.Now, in.HumanIdle, in.AgentsRunning = at(1, 10, 0), at(1, 19, 0), 40*time.Minute, 1
		in.LastActive = at(1, 18, 59)
		in.WrapUpStartedAt = at(1, 19, 0)
	}
	tests := []struct {
		name string
		in   Inputs
		want Verdict
	}{
		{"1 human active", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.HumanIdle = at(1, 8, 0), at(1, 12, 0), 5*time.Minute
		}), Verdict{Keep, "human-active"}},
		{"2 ok", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.HumanIdle, in.AgentsRunning, in.LastActive = at(1, 8, 0), at(1, 12, 0), 3*time.Hour, 1, at(1, 11, 58)
		}), Verdict{Keep, "ok"}},
		{"3 idle delete", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.HumanIdle, in.LastActive = at(1, 8, 0), at(1, 12, 0), 3*time.Hour, at(1, 9, 30)
		}), Verdict{Delete, "idle"}},
		{"4 max-uptime wrapup", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.HumanIdle, in.AgentsRunning, in.LastActive = at(1, 8, 0), at(1, 18, 1), 40*time.Minute, 1, at(1, 18, 0)
		}), Verdict{StartWrapUp, "max-uptime"}},
		{"5 evening-cutoff wrapup", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.HumanIdle, in.AgentsRunning, in.LastActive = at(1, 10, 0), at(1, 19, 0), 40*time.Minute, 1, at(1, 18, 59)
		}), Verdict{StartWrapUp, "evening-cutoff"}},
		{"6 wrapup in progress", mk(func(in *Inputs) { wrap(in); in.Now = at(1, 19, 3) }), Verdict{Keep, "wrapup-in-progress"}},
		{"7 grace elapsed", mk(func(in *Inputs) { wrap(in); in.Now = at(1, 19, 5) }), Verdict{Delete, "wrapup-grace-elapsed"}},
		{"8 no agents", mk(func(in *Inputs) { wrap(in); in.Now = at(1, 19, 3); in.AgentsRunning = 0 }), Verdict{Delete, "no-agents"}},
		{"9 human returns", mk(func(in *Inputs) { wrap(in); in.Now = at(1, 19, 3); in.HumanIdle = time.Minute }), Verdict{Keep, "human-active"}},
		{"10 night", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.NightUntil, in.AgentsRunning, in.HumanIdle = at(1, 10, 0), at(1, 23, 0), at(2, 7, 0), 2, 4*time.Hour
			in.LastActive = at(1, 22, 59)
		}), Verdict{Keep, "night"}},
		{"11 agents done", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.NightUntil, in.AgentsRunning, in.HumanIdle = at(1, 10, 0), at(1, 23, 0), at(2, 7, 0), 0, 4*time.Hour
			in.LastActive = at(1, 22, 59)
		}), Verdict{Delete, "agents-done"}},
		{"12 night deadline", mk(func(in *Inputs) {
			// up the same morning (08:00 day 2) so it is not otherwise due; human long idle.
			in.UpAt, in.Now, in.NightUntil, in.AgentsRunning, in.HumanIdle = at(1, 22, 0), at(2, 7, 1), at(2, 7, 0), 1, 8*time.Hour
			in.LastActive = at(2, 7, 0)
		}), Verdict{StartWrapUp, "night-deadline"}},
		{"13 up after cutoff", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.HumanIdle, in.LastActive = at(1, 20, 0), at(1, 20, 45), 31*time.Minute, at(1, 20, 44)
		}), Verdict{Delete, "evening-cutoff"}},
		{"13b startup grace after cutoff", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.LastActive = at(1, 20, 0), at(1, 20, 5), at(1, 20, 0)
		}), Verdict{Keep, "ok"}},
		{"14 stop requested", mk(func(in *Inputs) {
			// up this morning, last active recent: only StopRequested matters.
			in.UpAt, in.Now, in.HumanIdle, in.AgentsRunning, in.LastActive, in.StopRequested = at(1, 8, 0), at(1, 12, 0), time.Minute, 1, at(1, 11, 59), true
		}), Verdict{StartWrapUp, "stop-requested"}},
		{"16 open session idle 3h", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.HumanIdle, in.LastActive = at(1, 8, 0), at(1, 14, 0), 3*time.Hour, at(1, 11, 0)
		}), Verdict{Delete, "idle"}},
		{"17 late, no night session", mk(func(in *Inputs) {
			in.UpAt, in.Now, in.HumanIdle, in.LastActive = at(1, 21, 0), at(1, 23, 30), 61*time.Minute, at(1, 22, 20)
		}), Verdict{Delete, "evening-cutoff"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.in); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecideDST(t *testing.T) {
	// 15: box clock is UTC; times cross the 2026-03-29 CET->CEST change.
	up := time.Date(2026, 3, 28, 7, 0, 0, 0, time.UTC)   // 08:00 CET
	now := time.Date(2026, 3, 29, 17, 0, 0, 0, time.UTC) // 19:00 CEST
	in := base()
	in.UpAt, in.Now, in.HumanIdle, in.AgentsRunning, in.LastActive = up, now, time.Hour, 1, now.Add(-time.Minute)
	_, reason := Deadline(up, in.Limits, paris)
	if got := Decide(in); got != (Verdict{StartWrapUp, reason}) {
		t.Errorf("got %+v, want StartWrapUp %s", got, reason)
	}

	up2 := time.Date(2026, 3, 29, 8, 0, 0, 0, paris)
	d, r := Deadline(up2, in.Limits, paris)
	if want := time.Date(2026, 3, 29, 18, 0, 0, 0, paris); !d.Equal(want) || r != "max-uptime" {
		t.Errorf("Deadline = %v %s, want %v max-uptime", d, r, want)
	}
}

func TestInNightHours(t *testing.T) {
	for _, tc := range []struct {
		h, m int
		want bool
	}{{23, 0, true}, {6, 59, true}, {7, 0, false}, {12, 0, false}} {
		if got := InNightHours(at(1, tc.h, tc.m), "22:00-07:00", paris); got != tc.want {
			t.Errorf("%02d:%02d = %v, want %v", tc.h, tc.m, got, tc.want)
		}
	}
	if InNightHours(at(1, 23, 0), "garbage", paris) {
		t.Error("unparsable night_hours must be not-night")
	}
}

func TestStopWithUnknownAgentsWrapsUp(t *testing.T) {
	in := base()
	in.Now, in.UpAt, in.LastActive = at(1, 12, 0), at(1, 10, 0), at(1, 11, 59)
	in.StopRequested, in.AgentsRunning, in.AgentsUnknown = true, 0, true
	if v := Decide(in); v != (Verdict{StartWrapUp, "stop-requested"}) {
		t.Fatalf("start: %+v", v)
	}
	in.WrapUpStartedAt = in.Now.Add(-time.Minute)
	if v := Decide(in); v != (Verdict{Keep, "wrapup-in-progress"}) {
		t.Fatalf("in progress: %+v", v)
	}
	in.WrapUpStartedAt = in.Now.Add(-time.Duration(in.Limits.WrapUpGrace))
	if v := Decide(in); v != (Verdict{Delete, "wrapup-grace-elapsed"}) {
		t.Fatalf("elapsed: %+v", v)
	}
}
