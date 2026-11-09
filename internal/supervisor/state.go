package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is what the supervisor remembers between runs.
type State struct {
	LastActive      time.Time `json:"last_active"`
	WrapUpStartedAt time.Time `json:"wrap_up_started_at"`
	WrapUpReason    string    `json:"wrap_up_reason"`
	LastVerdict     string    `json:"last_verdict"` // the last verdict's reason
}

// Paths are the files the supervisor reads and writes.
type Paths struct {
	State, Night, StopRequest, Heartbeat, WrapUpFlag, Events string
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
	}
}

type nightFile struct {
	Until time.Time `json:"until"`
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
		return st, fmt.Errorf("parse state %s: %w", path, err)
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
