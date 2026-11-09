// Package supervisor is the on-box loop that decides when the box deletes itself.
package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const noSession = time.Duration(1 << 62)

// HumanIdle returns the smallest time since the last read (atime) over the
// numeric entries of ptsDir (ptmx and other non-numeric entries are ignored),
// or 1<<62 if there are none. atime, unlike mtime, does not advance on program
// output, so an abandoned open SSH session does not count as activity.
func HumanIdle(ptsDir string, now time.Time) time.Duration {
	entries, err := os.ReadDir(ptsDir)
	if err != nil {
		return noSession
	}
	best := noSession
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		fi, err := os.Stat(filepath.Join(ptsDir, e.Name()))
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		idle := now.Sub(time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)))
		if idle < 0 {
			idle = 0
		}
		if idle < best {
			best = idle
		}
	}
	return best
}

// AgentCounter counts running agent workloads.
type AgentCounter interface {
	Count(ctx context.Context) (int, error)
}

// DockerAgents counts containers labelled jauntbox.agent=1.
type DockerAgents struct{}

// dockerTimeout bounds `docker ps` so a hung daemon cannot wedge a pass.
var dockerTimeout = 20 * time.Second

// Count runs `docker ps`. A box without docker has no agents.
func (DockerAgents) Count(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "ps", "-q", "--filter", "label=jauntbox.agent=1")
	cmd.WaitDelay = time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("docker ps: %w: %s", err, bytes.TrimSpace(errb.Bytes()))
	}
	n := 0
	for _, l := range bytes.Split(out.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			n++
		}
	}
	return n, nil
}
