// Package events defines the jauntbox event stream and its JSONL sink.
package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	BoxUpRequested   = "box.up_requested"
	BoxCreated       = "box.created"
	BoxDownRequested = "box.down_requested"
	// BoxDeleted is written just before the box asks to delete itself (the
	// power is cut right after); box.delete_failed follows it if the call fails.
	BoxDeleted      = "box.deleted"
	WrapUpStarted   = "wrapup.started"
	WrapUpCancelled = "wrapup.cancelled"
	NightAuthorised = "night.authorised"
	// SupervisorTick is emitted only when the verdict changes.
	SupervisorTick = "supervisor.tick"
	// AgentStarted: an agent run's container is up (fields run, repo, branch, mode).
	AgentStarted = "agent.started"
	// AgentStopped: a run was finished and its container removed (fields run,
	// repo, reason, exit_code, and fallback = the fallback's output when it ran).
	AgentStopped = "agent.stopped"
)

// Event is one entry in the event stream.
type Event struct {
	Time   time.Time         `json:"time"`
	Kind   string            `json:"kind"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Sink receives events.
type Sink interface {
	Emit(e Event) error
}

type discard struct{}

func (discard) Emit(Event) error { return nil }

// Discard drops every event.
var Discard Sink = discard{}

type jsonl struct {
	mu   sync.Mutex
	path string
}

// JSONLFile appends one JSON object per line to path, creating the parent
// directory (0700) and the file (0600) as needed. Each event is synced to disk
// before Emit returns.
func JSONLFile(path string) Sink { return &jsonl{path: path} }

func (j *jsonl) Emit(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	// The box can lose power at any moment (self-delete, safety net): without
	// a sync the file length can reach the disk before the data, leaving NULs.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
