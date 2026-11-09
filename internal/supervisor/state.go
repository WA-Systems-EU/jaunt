package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrCorruptState marks an unparsable state file.
var ErrCorruptState = errors.New("corrupt state file")

// State is what the supervisor remembers between runs.
type State struct {
	LastActive      time.Time `json:"last_active"`
	WrapUpStartedAt time.Time `json:"wrap_up_started_at"`
	WrapUpReason    string    `json:"wrap_up_reason"`
	LastVerdict     string    `json:"last_verdict"` // the last verdict's reason
}

// Paths are the files the supervisor reads and writes.
type Paths struct {
	State, Night, StopRequest, Heartbeat, WrapUpFlag, Events, Lock string
}

// DefaultPaths are the production locations.
func DefaultPaths() Paths {
	return Paths{
		State:       "/var/lib/jauntbox/state.json",
		Night:       "/var/lib/jauntbox/night.json",
		StopRequest: "/var/lib/jauntbox/stop-requested",
		Heartbeat:   "/var/lib/jauntbox/heartbeat",
		WrapUpFlag:  "/run/jauntbox/wrapup",
		Events:      "/home/.jauntbox/events.jsonl",
		Lock:        "/run/jauntbox/supervisor.lock",
	}
}

type nightFile struct {
	Until time.Time `json:"until"`
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp, 0o644)); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// LoadState reads the state; a missing file yields the zero State.
func LoadState(path string) (State, error) {
	var st State
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse state %s: %w: %v", path, ErrCorruptState, err)
	}
	return st, nil
}

// SaveState writes the state atomically.
func SaveState(path string, st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// LoadNight reads the night session end; a missing file yields the zero time.
func LoadNight(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	var n nightFile
	if err := json.Unmarshal(data, &n); err != nil {
		return time.Time{}, fmt.Errorf("parse night file %s: %w", path, err)
	}
	return n.Until, nil
}

// SaveNight writes the night session end.
func SaveNight(path string, until time.Time) error {
	data, err := json.Marshal(nightFile{Until: until})
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// MaxNight is the longest night session that can be authorised.
const MaxNight = 24 * time.Hour

// ValidateNightUntil rejects a night end in the past or more than MaxNight away.
func ValidateNightUntil(now, until time.Time) error {
	if !until.After(now) {
		return fmt.Errorf("--until %s is in the past; give a future time", until.Format(time.RFC3339))
	}
	if until.After(now.Add(MaxNight)) {
		return errors.New("night sessions are limited to 24h; pick an earlier --until")
	}
	return nil
}
