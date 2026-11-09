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

	"github.com/WA-Systems-EU/jaunt/internal/boxinfo"
	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/lifecycle"
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
}

// New wires the production supervisor for info.
func New(info boxinfo.BoxInfo) *Supervisor {
	p := DefaultPaths()
	return &Supervisor{
		Info:   info,
		Paths:  p,
		PtsDir: ptsDir,
		Agents: DockerAgents{},
		Events: events.JSONLFile(p.Events),
		Now:    time.Now,
		Sync:   syscall.Sync,
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

func (s *Supervisor) emit(kind string, fields map[string]string) {
	_ = s.Events.Emit(events.Event{Time: s.Now().UTC(), Kind: kind, Fields: fields})
}

// evaluate gathers the inputs and decides, without side effects.
// st.LastActive is refreshed in the returned copy. warn is non-nil when the
// agent count failed but the human is active, so the pass can go on without it.
func (s *Supervisor) evaluate(ctx context.Context, st State) (_ State, _ lifecycle.Verdict, warn error, err error) {
	loc, err := time.LoadLocation(s.Info.Timezone)
	if err != nil {
		return st, lifecycle.Verdict{}, nil, fmt.Errorf("load timezone %q from box info: %w", s.Info.Timezone, err)
	}
	now := s.Now()
	if st.LastActive.IsZero() {
		st.LastActive = s.Info.UpAt
	}
	night, err := LoadNight(s.Paths.Night)
	if err != nil {
		return st, lifecycle.Verdict{}, nil, err
	}
	idle := HumanIdle(s.PtsDir, now)
	agents, err := s.Agents.Count(ctx)
	if err != nil {
		// An active human keeps the box regardless of agents, so a broken or
		// hung docker must not starve the heartbeat and trip the safety net
		// under a working human. Otherwise fail the pass (no heartbeat).
		if idle > time.Duration(s.Info.Limits.HumanActiveWindow) {
			return st, lifecycle.Verdict{}, nil, fmt.Errorf("count agents: %w", err)
		}
		agents, warn = 0, fmt.Errorf("count agents: %w", err)
	}
	if idle <= time.Duration(s.Info.Limits.HumanActiveWindow) || agents > 0 {
		st.LastActive = now
	}
	_, statErr := os.Stat(s.Paths.StopRequest)
	v := lifecycle.Decide(lifecycle.Inputs{
		Now: now, UpAt: s.Info.UpAt, LastActive: st.LastActive,
		HumanIdle: idle, AgentsRunning: agents, NightUntil: night,
		StopRequested:   statErr == nil,
		WrapUpStartedAt: st.WrapUpStartedAt,
		Limits:          s.Info.Limits, Loc: loc,
	})
	return st, v, warn, nil
}

// loadState reads the state; a corrupt file falls back to the zero state
// (LastActive = UpAt) and, when warn is set, emits a warning event.
func (s *Supervisor) loadState(warn bool) (State, error) {
	st, err := LoadState(s.Paths.State)
	if errors.Is(err, ErrCorruptState) {
		if warn {
			s.emit("supervisor.warning", map[string]string{"error": err.Error()})
		}
		return State{}, nil
	}
	return st, err
}

// Preview returns the current state and verdict without any side effect.
func (s *Supervisor) Preview(ctx context.Context) (State, lifecycle.Verdict, error) {
	st, err := s.loadState(false)
	if err != nil {
		return st, lifecycle.Verdict{}, err
	}
	st, v, _, err := s.evaluate(ctx, st)
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
	st, err := s.loadState(true)
	if err != nil {
		return lifecycle.Verdict{}, err
	}
	st, v, warn, err := s.evaluate(ctx, st)
	if err != nil {
		return v, err
	}
	if warn != nil {
		s.emit("supervisor.warning", map[string]string{"error": warn.Error()})
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
		s.emit(events.WrapUpStarted, fields)
	case v.Action == lifecycle.Keep && v.Reason == "wrapup-in-progress":
		if _, err := os.Stat(s.Paths.WrapUpFlag); os.IsNotExist(err) {
			if err := writeFlag(); err != nil {
				return v, err
			}
		}
	case v.Action == lifecycle.Keep && !st.WrapUpStartedAt.IsZero():
		st.WrapUpStartedAt, st.WrapUpReason = time.Time{}, ""
		if err := os.Remove(s.Paths.WrapUpFlag); err != nil && !os.IsNotExist(err) {
			return v, err
		}
		s.emit(events.WrapUpCancelled, fields)
	}
	if v.Reason != st.LastVerdict {
		s.emit(events.SupervisorTick, fields)
	}
	st.LastVerdict = v.Reason
	if err := SaveState(s.Paths.State, st); err != nil {
		return v, err
	}
	if v.Action == lifecycle.Delete {
		s.Sync()
		if err := s.DeleteSelf(ctx); err != nil {
			s.emit("box.delete_failed", map[string]string{"reason": v.Reason, "error": err.Error()})
			return v, err
		}
		s.emit(events.BoxDeleted, fields)
		return v, nil
	}
	return v, s.touchHeartbeat(now)
}
