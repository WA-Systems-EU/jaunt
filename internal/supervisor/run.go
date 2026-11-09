package supervisor

import (
	"context"
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
// st.LastActive is refreshed in the returned copy.
func (s *Supervisor) evaluate(ctx context.Context, st State) (State, lifecycle.Verdict, error) {
	loc, err := time.LoadLocation(s.Info.Timezone)
	if err != nil {
		return st, lifecycle.Verdict{}, fmt.Errorf("load timezone %q from box info: %w", s.Info.Timezone, err)
	}
	now := s.Now()
	if st.LastActive.IsZero() {
		st.LastActive = s.Info.UpAt
	}
	night, err := LoadNight(s.Paths.Night)
	if err != nil {
		return st, lifecycle.Verdict{}, err
	}
	agents, err := s.Agents.Count(ctx)
	if err != nil {
		return st, lifecycle.Verdict{}, fmt.Errorf("count agents: %w", err)
	}
	idle := HumanIdle(s.PtsDir, now)
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
	return st, v, nil
}

// Preview returns the current state and verdict without any side effect.
func (s *Supervisor) Preview(ctx context.Context) (State, lifecycle.Verdict, error) {
	st, err := LoadState(s.Paths.State)
	if err != nil {
		return st, lifecycle.Verdict{}, err
	}
	return s.evaluate(ctx, st)
}

// RunOnce performs one supervision pass.
func (s *Supervisor) RunOnce(ctx context.Context) (lifecycle.Verdict, error) {
	now := s.Now()
	if err := os.MkdirAll(filepath.Dir(s.Paths.Heartbeat), 0o755); err != nil {
		return lifecycle.Verdict{}, err
	}
	if err := os.WriteFile(s.Paths.Heartbeat, nil, 0o644); err != nil {
		return lifecycle.Verdict{}, err
	}
	if err := os.Chtimes(s.Paths.Heartbeat, now, now); err != nil {
		return lifecycle.Verdict{}, err
	}
	st, err := LoadState(s.Paths.State)
	if err != nil {
		return lifecycle.Verdict{}, err
	}
	st, v, err := s.evaluate(ctx, st)
	if err != nil {
		return v, err
	}
	fields := map[string]string{"reason": v.Reason}

	switch {
	case v.Action == lifecycle.StartWrapUp:
		st.WrapUpStartedAt, st.WrapUpReason = now, v.Reason
		if err := os.MkdirAll(filepath.Dir(s.Paths.WrapUpFlag), 0o755); err != nil {
			return v, err
		}
		if err := os.WriteFile(s.Paths.WrapUpFlag, []byte(v.Reason+"\n"), 0o644); err != nil {
			return v, err
		}
		s.emit(events.WrapUpStarted, fields)
	case v.Action == lifecycle.Keep && v.Reason != "wrapup-in-progress" && !st.WrapUpStartedAt.IsZero():
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
		s.emit(events.BoxDeleted, fields)
		s.Sync()
		if err := s.DeleteSelf(ctx); err != nil {
			return v, err
		}
	}
	return v, nil
}
