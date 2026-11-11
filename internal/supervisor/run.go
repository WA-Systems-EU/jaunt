package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // a box without tzdata must not fail every pass

	"github.com/WA-Systems-EU/jaunt/internal/agentrun"
	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/lifecycle"
	"github.com/WA-Systems-EU/jaunt/internal/minter"
	"github.com/WA-Systems-EU/jaunt/internal/provider/hetzner"
)

const (
	tokenPath = "/etc/jauntbox/hcloud.token"
	ptsDir    = "/dev/pts"
)

// Supervisor decides, on the box, whether the box should be deleted.
type Supervisor struct {
	Info       boxinfo.BoxInfo
	Paths      Paths
	PtsDir     string
	Agents     AgentCounter
	Events     events.Sink
	Now        func() time.Time
	DeleteSelf func(ctx context.Context) error
	Sync       func()
	// Revoke drops cached forge tokens before the box deletes itself; nil is a no-op.
	Revoke func(ctx context.Context) error
	// Runs drives the agent runs (token refresh, max duration, finish); nil is a no-op.
	Runs RunManager
}

// RunManager is what the supervisor needs from the agent runs.
type RunManager interface {
	Tick(ctx context.Context, now time.Time) error
	WrapUpAll(now time.Time, reason string) error
	FinishAll(ctx context.Context) error
	CancelWrapUps(reason string) error
}

// RevokeTimeout bounds the token revoke before the box deletes itself.
const RevokeTimeout = 30 * time.Second

// TimerInterval is the supervise timer's OnUnitActiveSec and PassTimeout
// the supervise unit's TimeoutStartSec (systemd kills a longer pass). They
// must match internal/cloudinit/units (a test checks); the laptop's
// `jauntbox down` sizes its wait from them.
const (
	TimerInterval = 2 * time.Minute
	PassTimeout   = 600 * time.Second
)

// MaxPassBudget is the worst case of a Delete pass spent on agent work and
// revoke: Tick + FinishAll + Revoke. TimeoutStartSec of the supervise unit
// must exceed it with margin. The heartbeat is touched at the END of a
// successful non-Delete pass (never earlier, so a failed pass still lets
// the safety net act); such a pass spends at most the Tick budget, far
// below the safety net's 900 s.
//
// Each run's teardown has its own agentrun.TeardownTimeout outside those
// deadlines, and a run can be torn down by both Tick and FinishAll, so the
// budget adds 2 teardowns per run.
func MaxPassBudget(runs int) time.Duration {
	return agentrun.TickTimeout + agentrun.FinishAllTimeout + RevokeTimeout +
		time.Duration(2*runs)*agentrun.TeardownTimeout
}

// New wires the production supervisor for info.
func New(info boxinfo.BoxInfo) *Supervisor {
	p := DefaultPaths()
	ev := events.JSONLFile(p.Events)
	mn := minter.New(ev)
	return &Supervisor{
		Info:   info,
		Paths:  p,
		PtsDir: ptsDir,
		Agents: DockerAgents{},
		Events: ev,
		Now:    time.Now,
		Sync:   syscall.Sync,
		Revoke: func(ctx context.Context) error {
			_, err := mn.RevokeAll(ctx)
			return err
		},
		Runs: &agentrun.Maintainer{
			Docker: agentrun.NewCLI(), Paths: agentrun.DefaultPaths(), Mint: mn.Mint,
			Events: ev, Grace: time.Duration(info.Limits.WrapUpGrace),
		},
		DeleteSelf: func(ctx context.Context) error {
			tok, err := os.ReadFile(tokenPath)
			if err != nil {
				return fmt.Errorf("read hetzner token: %w", err)
			}
			id, err := hetzner.SelfID(ctx, "")
			if err != nil {
				return err
			}
			return hetzner.New(strings.TrimSpace(string(tok))).DeleteBox(ctx, id)
		},
	}
}

func (s *Supervisor) emit(now time.Time, kind string, fields map[string]string) {
	_ = s.Events.Emit(events.Event{Time: now.UTC(), Kind: kind, Fields: fields})
}

// evaluate gathers the inputs and decides, without side effects.
// st.LastActive is refreshed in the returned copy. warn is non-nil when the
// agent count failed but the human is active, so the pass can go on without it.
func (s *Supervisor) evaluate(ctx context.Context, now time.Time, st State) (_ State, _ lifecycle.Verdict, warn error, err error) {
	loc, err := time.LoadLocation(s.Info.Timezone)
	if err != nil {
		return st, lifecycle.Verdict{}, nil, fmt.Errorf("load timezone %q from box info: %w", s.Info.Timezone, err)
	}
	if st.LastActive.IsZero() {
		st.LastActive = s.Info.UpAt
	}
	night, err := LoadNight(s.Paths.Night)
	if err != nil {
		return st, lifecycle.Verdict{}, nil, err
	}
	idle := HumanIdle(s.PtsDir, now)
	agents, err := s.Agents.Count(ctx)
	unknown := false
	if err != nil {
		// An active human keeps the box regardless of agents, so a broken or
		// hung docker must not starve the heartbeat and trip the safety net
		// under a working human. Otherwise fail the pass (no heartbeat).
		if idle > time.Duration(s.Info.Limits.HumanActiveWindow) {
			return st, lifecycle.Verdict{}, nil, fmt.Errorf("count agents: %w", err)
		}
		agents, unknown, warn = 0, true, fmt.Errorf("count agents: %w", err)
	}
	if idle <= time.Duration(s.Info.Limits.HumanActiveWindow) {
		// Measure idleness from the real last input, not from this pass.
		if last := now.Add(-idle); last.After(st.LastActive) {
			st.LastActive = last
		}
	}
	if agents > 0 {
		st.LastActive = now
	}
	_, statErr := os.Stat(s.Paths.StopRequest)
	v := lifecycle.Decide(lifecycle.Inputs{
		Now: now, UpAt: s.Info.UpAt, LastActive: st.LastActive,
		HumanIdle: idle, AgentsRunning: agents, AgentsUnknown: unknown, NightUntil: night,
		StopRequested:   statErr == nil,
		WrapUpStartedAt: st.WrapUpStartedAt,
		Limits:          s.Info.Limits, Loc: loc,
	})
	return st, v, warn, nil
}

// loadState reads the state; a corrupt file falls back to the zero state
// (LastActive = UpAt) and, when warn is set, emits a warning event.
func (s *Supervisor) loadState(now time.Time, warn bool) (State, error) {
	st, err := LoadState(s.Paths.State)
	if errors.Is(err, ErrCorruptState) {
		if warn {
			s.emit(now, "supervisor.warning", map[string]string{"error": err.Error()})
		}
		return State{}, nil
	}
	return st, err
}

// Preview returns the current state and verdict without any side effect.
func (s *Supervisor) Preview(ctx context.Context) (State, lifecycle.Verdict, error) {
	now := s.Now()
	st, err := s.loadState(now, false)
	if err != nil {
		return st, lifecycle.Verdict{}, err
	}
	st, v, _, err := s.evaluate(ctx, now, st)
	return st, v, err
}

func (s *Supervisor) touchHeartbeat(now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(s.Paths.Heartbeat), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.Paths.Heartbeat, nil, 0o644); err != nil {
		return err
	}
	return os.Chtimes(s.Paths.Heartbeat, now, now)
}

// ErrBusy means another supervisor pass holds the lock.
var ErrBusy = errors.New("another supervisor run is in progress")

func (s *Supervisor) lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.Paths.Lock), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.Paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrBusy
	}
	return func() { f.Close() }, nil
}

// RunOnce performs one supervision pass. The heartbeat is touched only as the
// last step of a successful non-Delete pass, so any persistent failure lets
// the heartbeat go stale and the external safety net take over.
func (s *Supervisor) RunOnce(ctx context.Context) (lifecycle.Verdict, error) {
	unlock, err := s.lock()
	if err != nil {
		return lifecycle.Verdict{}, err
	}
	defer unlock()
	now := s.Now()
	st, err := s.loadState(now, true)
	if err != nil {
		return lifecycle.Verdict{}, err
	}
	// Drive the agent runs first so a finished run is not counted as active.
	// A failure here never blocks the lifecycle decision or the heartbeat.
	if s.Runs != nil {
		if err := safeCall("runs tick", func() error { return s.Runs.Tick(ctx, now) }); err != nil {
			s.emit(now, "agent.tick_failed", map[string]string{"error": err.Error()})
		}
	}
	st, v, warn, err := s.evaluate(ctx, now, st)
	if err != nil {
		return v, err
	}
	if warn != nil {
		s.emit(now, "supervisor.warning", map[string]string{"error": warn.Error()})
	}
	fields := map[string]string{"reason": v.Reason}
	writeFlag := func() error {
		if err := os.MkdirAll(filepath.Dir(s.Paths.WrapUpFlag), 0o755); err != nil {
			return err
		}
		return os.WriteFile(s.Paths.WrapUpFlag, []byte(st.WrapUpReason+"\n"), 0o644)
	}

	switch {
	case v.Action == lifecycle.StartWrapUp:
		st.WrapUpStartedAt, st.WrapUpReason = now, v.Reason
		if err := writeFlag(); err != nil {
			return v, err
		}
		s.emit(now, events.WrapUpStarted, fields)
		if s.Runs != nil {
			if err := safeCall("runs wrap-up", func() error { return s.Runs.WrapUpAll(now, v.Reason) }); err != nil {
				s.emit(now, "agent.wrapup_failed", map[string]string{"error": err.Error()})
			}
		}
	case v.Action == lifecycle.Keep && v.Reason == "wrapup-in-progress":
		if _, err := os.Stat(s.Paths.WrapUpFlag); os.IsNotExist(err) {
			if err := writeFlag(); err != nil {
				return v, err
			}
		}
	case v.Action == lifecycle.Keep && !st.WrapUpStartedAt.IsZero():
		cancelled := st.WrapUpReason
		st.WrapUpStartedAt, st.WrapUpReason = time.Time{}, ""
		if err := os.Remove(s.Paths.WrapUpFlag); err != nil && !os.IsNotExist(err) {
			return v, err
		}
		s.emit(now, events.WrapUpCancelled, fields)
		if s.Runs != nil {
			// Reason that was being cancelled, not the new verdict's.
			if err := safeCall("runs cancel wrap-up", func() error { return s.Runs.CancelWrapUps(cancelled) }); err != nil {
				s.emit(now, "agent.wrapup_cancel_failed", map[string]string{"error": err.Error()})
			}
		}
	}
	if v.Reason != st.LastVerdict {
		s.emit(now, events.SupervisorTick, fields)
	}
	st.LastVerdict = v.Reason
	if err := SaveState(s.Paths.State, st); err != nil {
		return v, err
	}
	if v.Action == lifecycle.Delete {
		// Teardown order: finish the runs (their fallback pushes, so it needs
		// the tokens) -> revoke tokens -> box.deleted -> sync -> self-delete.
		if s.Runs != nil {
			fctx, cancel := context.WithTimeout(ctx, agentrun.FinishAllTimeout)
			err := safeCall("finish runs", func() error { return s.Runs.FinishAll(fctx) })
			cancel()
			if err != nil {
				s.emit(now, "agent.finish_failed", map[string]string{"error": err.Error()})
			}
		}
		if s.Revoke != nil {
			rctx, cancel := context.WithTimeout(ctx, RevokeTimeout)
			err := safeRevoke(rctx, s.Revoke)
			cancel()
			if err != nil {
				s.emit(now, "token.revoke_failed", map[string]string{"error": err.Error()})
			}
		}
		// Hetzner cuts power soon after DeleteSelf returns, so nothing written
		// after it reaches the volume: record box.deleted first, then sync. A
		// failed delete follows it with box.delete_failed.
		s.emit(now, events.BoxDeleted, fields)
		s.Sync()
		if err := s.DeleteSelf(ctx); err != nil {
			s.emit(now, "box.delete_failed", map[string]string{"reason": v.Reason, "error": err.Error()})
			return v, err
		}
		return v, nil
	}
	return v, s.touchHeartbeat(now)
}

// safeRevoke turns a panic in the revoke hook into an error so the box still
// syncs and deletes itself.
func safeRevoke(ctx context.Context, revoke func(context.Context) error) error {
	return safeCall("revoke", func() error { return revoke(ctx) })
}

// safeCall runs f, turning a panic into an error.
func safeCall(what string, f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panicked: %v", what, r)
		}
	}()
	return f()
}
