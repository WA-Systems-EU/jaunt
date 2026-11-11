package agentrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Run is one agent run, stored as run.json in its volume directory.
type Run struct {
	ID            string `json:"id"`
	Repo          string `json:"repo"`
	Checkout      string `json:"checkout"`
	Branch        string `json:"branch"`
	DefaultBranch string `json:"default_branch"`
	Mode          string `json:"mode"` // "interactive" or "unattended"
	Prompt        string `json:"prompt,omitempty"`
	// SkipPermissions is the interactive permissions choice; attach reuses it.
	SkipPermissions bool          `json:"skip_permissions"`
	StartedAt       time.Time     `json:"started_at"`
	Max             time.Duration `json:"max"`
	WrapUpAt        time.Time     `json:"wrapup_at,omitzero"`
	WrapUpReason    string        `json:"wrapup_reason,omitempty"`
	FinishedAt      time.Time     `json:"finished_at,omitzero"`
	ExitCode        int           `json:"exit_code"`
}

const (
	ModeInteractive = "interactive"
	ModeUnattended  = "unattended"
)

// ContainerName is the container (and network) name of run id.
func ContainerName(id string) string { return "jauntbox-" + id }

// Paths are where run state lives on the box.
type Paths struct {
	RunsTmp string // per-run tmpfs state (token, wrapup) and env files
	RunsVol string // per-run record and logs on the volume
	Agents  string // per-repo agent homes on the volume
}

// DefaultPaths are the production locations.
func DefaultPaths() Paths {
	return Paths{RunsTmp: "/run/jauntbox/runs", RunsVol: "/home/.jauntbox/runs", Agents: "/home/.jauntbox/agents"}
}

// NewRunID is <repo name lowercased>-<UTC YYYYMMDD-HHMMSS>.
func NewRunID(repoName string, t time.Time) string {
	return strings.ToLower(repoName) + "-" + t.UTC().Format("20060102-150405")
}

var idRE = regexp.MustCompile(`^[a-z0-9_.][a-z0-9_.-]*$`)

// validID guards ids used as path components and container names.
func validID(id string) bool {
	return len(id) <= 128 && idRE.MatchString(id) && !strings.Contains(id, "..")
}

var runIDRE = regexp.MustCompile(`^[a-z0-9_.][a-z0-9_.-]*-[0-9]{8}-[0-9]{6}$`)

// ValidRunID reports whether id has the run id format
// (<repo name lowercased>-<YYYYMMDD>-<HHMMSS>); check ids from the command
// line with it before building paths or container names from them.
func ValidRunID(id string) bool { return validID(id) && runIDRE.MatchString(id) }

func (p Paths) runDir(id string) string { return filepath.Join(p.RunsVol, id) }

// Load reads the record of run id.
func (p Paths) Load(id string) (Run, error) {
	var r Run
	if !validID(id) {
		return r, fmt.Errorf("invalid run id %q", id)
	}
	b, err := os.ReadFile(filepath.Join(p.runDir(id), "run.json"))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("run %s: %w", id, err)
	}
	return r, nil
}

// Save writes run.json (root, 0600) atomically, creating the run directory.
func (p Paths) Save(r Run) error {
	if !validID(r.ID) {
		return fmt.Errorf("invalid run id %q", r.ID)
	}
	dir := p.runDir(r.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".run.json.tmp")
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "run.json"))
}

// All returns every readable saved run, oldest first; see Scan.
func (p Paths) All() ([]Run, error) {
	runs, _, err := p.Scan()
	return runs, err
}

// Scan returns every saved run, oldest first, and the ids whose run.json
// could not be read or parsed (skipped, so one bad record blocks nothing).
// Directories without run.json (a start that failed before saving) are
// neither. Only an unreadable RunsVol is an error.
func (p Paths) Scan() (runs []Run, bad []string, err error) {
	ents, err := os.ReadDir(p.RunsVol)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range ents {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		r, err := p.Load(e.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || r.ID != e.Name() {
			bad = append(bad, e.Name())
			continue
		}
		runs = append(runs, r)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
	return runs, bad, nil
}
