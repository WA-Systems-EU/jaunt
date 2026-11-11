package agentrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/WA-Systems-EU/jaunt/internal/events"
	"github.com/WA-Systems-EU/jaunt/internal/forge"
)

// Events a Maintainer emits besides agent.stopped (from Finish).
const (
	evWrapUpStarted      = "agent.wrapup_started"
	evFinished           = "agent.finished"
	evTokenRefreshed     = "agent.token_refreshed"
	evTokenRefreshFailed = "agent.token_refresh_failed"
	evTickFailed         = "agent.tick_failed"
	evRunCorrupt         = "agent.run_corrupt"
	evFinishFailed       = "agent.finish_failed"
	evWrapUpFailed       = "agent.wrapup_failed"
	evPruned             = "agent.pruned"
	wrapUpReasonRunMax   = "run-max"
	tokenFileMode        = 0o640
)

// TickTimeout is the overall deadline of one Tick; FinishAllTimeout that of
// FinishAll (all runs together; each run's fallback gets what is left of it,
// capped by FallbackTimeout). They bound a supervisor pass; the supervise
// unit's TimeoutStartSec must exceed them plus the revoke timeout plus, per
// run, two TeardownTimeouts (each Finish tears down on its own detached
// deadline, and a run can be torn down by both Tick and FinishAll); see
// supervisor.MaxPassBudget.
//
// MinFinishBudget is the least a grace-elapsed finish needs left on the Tick
// deadline to start: with less, the fallback's push could be cut short and
// the run end without it, so the finish is left due for the next pass (or
// FinishAll). A var only so tests can scale it with TickTimeout.
var (
	TickTimeout      = 2 * time.Minute
	FinishAllTimeout = 5 * time.Minute
	MinFinishBudget  = 60 * time.Second
)

// RunRetention is how long a finished run's record and logs are kept, and
// RunKeepMin the number of most recently finished runs kept whatever their
// age, so `agent list` never goes empty on a quiet box.
const (
	RunRetention = 30 * 24 * time.Hour
	RunKeepMin   = 20
)

// BoxReasonPrefix marks wrap-ups requested for the whole box, so cancelling
// the box wrap-up leaves `agent stop` and run-max wrap-ups alone (the box
// verdict also uses "stop-requested").
const BoxReasonPrefix = "box-"

// Maintainer is the supervisor's per-pass driver of the active agent runs.
type Maintainer struct {
	Docker Docker
	Paths  Paths
	Mint   func(ctx context.Context, repo string) (forge.Token, error)
	Events events.Sink
	Grace  time.Duration
}

func (m *Maintainer) emit(now time.Time, kind string, fields map[string]string) {
	if m.Events != nil {
		_ = m.Events.Emit(events.Event{Time: now.UTC(), Kind: kind, Fields: fields})
	}
}

// active returns the runs without FinishedAt and reports unreadable records.
func (m *Maintainer) active(now time.Time) ([]Run, error) {
	runs, bad, err := m.Paths.Scan()
	if err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		m.emit(now, evRunCorrupt, map[string]string{"runs": strings.Join(bad, ",")})
	}
	var out []Run
	for _, r := range runs {
		if r.FinishedAt.IsZero() {
			out = append(out, r)
		}
	}
	return out, nil
}

// Tick drives every active run once: a gone or exited container is
// finished (no fallback; an unattended run that exited also emits
// agent.finished with its exit code); a running one gets its token
// refreshed, is asked to wrap up at its max duration, and is finished once
// the grace after the wrap-up request has elapsed and at least
// MinFinishBudget is left of the Tick deadline. A failure of one run is
// reported as an event and does not stop the others; only an unreadable
// runs directory is an error.
func (m *Maintainer) Tick(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, TickTimeout)
	defer cancel()
	runs, err := m.active(now)
	if err != nil {
		return err
	}
	// Cheap work (token refresh, wrap-up requests, finishing gone or exited
	// containers) goes first for every run; the finishes that run a fallback
	// come last, so a slow fallback cannot starve the other runs of it.
	var late []func()
	for _, r := range runs {
		if f := m.tickRun(ctx, now, r); f != nil {
			late = append(late, f)
		}
	}
	for _, f := range late {
		if dl, ok := ctx.Deadline(); ctx.Err() != nil || ok && time.Until(dl) < MinFinishBudget {
			break // out of time: the grace is still elapsed next pass
		}
		f()
	}
	m.prune(now)
	return nil
}

// prune removes the record and logs of finished runs whose FinishedAt is
// older than RunRetention, except the RunKeepMin most recently finished.
// Unfinished runs are never touched. The path is built from an id that Scan
// validated (a single path component equal to the run.json's own id) under
// RunsVol, whose parents are root's, and os.RemoveAll unlinks a symlink
// instead of following it; the log subdirectory is the user's, so a link
// planted there is removed, not entered. Failures are best effort and retried
// next pass.
func (m *Maintainer) prune(now time.Time) {
	runs, _, err := m.Paths.Scan()
	if err != nil {
		return
	}
	var done []Run
	for _, r := range runs {
		if !r.FinishedAt.IsZero() {
			done = append(done, r)
		}
	}
	if len(done) <= RunKeepMin {
		return
	}
	sort.SliceStable(done, func(i, j int) bool { return done[i].FinishedAt.After(done[j].FinishedAt) })
	var gone []string
	for _, r := range done[RunKeepMin:] {
		if now.Sub(r.FinishedAt) <= RunRetention {
			continue
		}
		if os.RemoveAll(m.Paths.runDir(r.ID)) == nil {
			gone = append(gone, r.ID)
		}
	}
	if n := len(gone); n > 0 {
		m.emit(now, evPruned, map[string]string{"count": strconv.Itoa(n), "runs": strings.Join(gone, ",")})
	}
}

// tickRun does the run's cheap work and returns the grace-elapsed finish, if
// due, for the caller to run later.
func (m *Maintainer) tickRun(ctx context.Context, now time.Time, r Run) func() {
	fail := func(kind string, err error) {
		m.emit(now, kind, map[string]string{"run": r.ID, "repo": r.Repo, "error": err.Error()})
	}
	st, err := m.Docker.Inspect(ctx, ContainerName(r.ID))
	if err != nil {
		fail(evTickFailed, err)
		return nil
	}
	finish := func() error {
		err := Finish(ctx, m.Docker, m.Paths, r.ID, m.Events, now)
		if err != nil {
			fail(evFinishFailed, err)
		}
		return err
	}
	if !st.Running {
		if finish() == nil && st.Exists && r.Mode == ModeUnattended {
			m.emit(now, evFinished, map[string]string{"run": r.ID, "repo": r.Repo, "exit_code": strconv.Itoa(st.ExitCode)})
		}
		return nil
	}
	if err := m.refreshToken(ctx, now, r); err != nil {
		fail(evTokenRefreshFailed, err)
	}
	if r.WrapUpAt.IsZero() {
		if r.Max > 0 && !now.Before(r.StartedAt.Add(r.Max)) {
			if err := RequestWrapUp(m.Paths, r.ID, wrapUpReasonRunMax, now); err != nil {
				fail(evWrapUpFailed, err)
				return nil
			}
			m.emit(now, evWrapUpStarted, map[string]string{"run": r.ID, "repo": r.Repo, "reason": wrapUpReasonRunMax})
		}
		return nil
	}
	if !now.Before(r.WrapUpAt.Add(m.Grace)) {
		return func() { _ = finish() }
	}
	return nil
}

// refreshToken mints a token for the run's repo and, when it differs from
// the one in the run's token file, replaces the file (temp + rename in the
// same directory). The file keeps the group of the current one (the box
// user's, set by Start) and mode 0640, root-owned.
func (m *Maintainer) refreshToken(ctx context.Context, now time.Time, r Run) error {
	dir := filepath.Join(m.Paths.RunsTmp, r.ID)
	path := filepath.Join(dir, "token")
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // no token file for this run: nothing to refresh, no mint
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	tok, err := m.Mint(ctx, r.Repo)
	if err != nil {
		return err
	}
	if tok.Value == "" || strings.ContainsAny(tok.Value, "\r\n\x00") {
		return errors.New("the minted token is malformed")
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == tok.Value {
		return nil
	}
	gid := -1
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		gid = int(sys.Gid)
	}
	tmp := filepath.Join(dir, ".token.tmp")
	_ = os.Remove(tmp)
	if err := writeNew(tmp, []byte(tok.Value), tokenFileMode, -1, gid); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	m.emit(now, evTokenRefreshed, map[string]string{"run": r.ID, "repo": r.Repo})
	return nil
}

// WrapUpAll asks every active run that has not been asked yet to wrap up,
// with the box reason prefixed by BoxReasonPrefix.
func (m *Maintainer) WrapUpAll(now time.Time, reason string) error {
	runs, err := m.active(now)
	if err != nil {
		return err
	}
	var errs []error
	reason = BoxReasonPrefix + reason
	for _, r := range runs {
		if !r.WrapUpAt.IsZero() {
			continue
		}
		if err := RequestWrapUp(m.Paths, r.ID, reason, now); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", r.ID, err))
			continue
		}
		m.emit(now, evWrapUpStarted, map[string]string{"run": r.ID, "repo": r.Repo, "reason": reason})
	}
	return errors.Join(errs...)
}

// FinishAll finishes every active run (fallback first while running).
func (m *Maintainer) FinishAll(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, FinishAllTimeout)
	defer cancel()
	now := time.Now()
	runs, err := m.active(now)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range runs {
		if err := Finish(ctx, m.Docker, m.Paths, r.ID, m.Events, time.Now()); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

// CancelWrapUps undoes the wrap-ups WrapUpAll requested for the box reason:
// every active run whose wrap-up reason is the box-prefixed reason loses its
// WrapUpAt, WrapUpReason and wrapup flag. Other wrap-ups are kept.
func (m *Maintainer) CancelWrapUps(reason string) error {
	runs, err := m.active(time.Now())
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range runs {
		if r.WrapUpAt.IsZero() || r.WrapUpReason != BoxReasonPrefix+reason {
			continue
		}
		r.WrapUpAt, r.WrapUpReason = time.Time{}, ""
		if err := m.Paths.Save(r); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", r.ID, err))
			continue
		}
		if err := os.Remove(filepath.Join(m.Paths.RunsTmp, r.ID, "wrapup")); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("run %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}
