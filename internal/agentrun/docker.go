package agentrun

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Docker is the root Docker daemon as agent runs need it.
type Docker interface {
	ImageExists(ctx context.Context, tag string) (bool, error)
	Build(ctx context.Context, tag string, contextTar []byte) error
	NetworkCreate(ctx context.Context, name string, labels map[string]string) error
	NetworkRemove(ctx context.Context, name string) error
	Run(ctx context.Context, spec ContainerSpec) error
	Exec(ctx context.Context, container string, user string, args ...string) ([]byte, error)
	Inspect(ctx context.Context, name string) (State, error)
	Logs(ctx context.Context, name string, tail int) (string, error)
	Remove(ctx context.Context, name string) error
	Stop(ctx context.Context, name string, timeout time.Duration) error
	ListRuns(ctx context.Context) ([]string, error)
}

// State is what Inspect reports about a container.
type State struct {
	Exists, Running bool
	ExitCode        int
	IP              string
}

// Mount is a bind mount.
type Mount struct {
	Src, Dst string
	RO       bool
}

// ContainerSpec is a detached agent container.
type ContainerSpec struct {
	Name, Hostname, Network string
	UID, GID                int
	Labels                  []string // k=v, in order
	EnvFile                 string
	Env                     []string // k=v, passed with -e
	Mounts                  []Mount
	Image                   string
	Cmd                     []string // empty: the image's CMD
}

// Args is the docker argv (after "docker") that starts the container.
func (s ContainerSpec) Args() []string {
	a := []string{"run", "-d", "--name", s.Name, "--hostname", s.Hostname, "--network", s.Network,
		"--user", strconv.Itoa(s.UID) + ":" + strconv.Itoa(s.GID),
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--pids-limit", "4096"}
	for _, l := range s.Labels {
		a = append(a, "--label", l)
	}
	if s.EnvFile != "" {
		a = append(a, "--env-file", s.EnvFile)
	}
	for _, e := range s.Env {
		a = append(a, "-e", e)
	}
	for _, m := range s.Mounts {
		v := m.Src + ":" + m.Dst
		if m.RO {
			v += ":ro"
		}
		a = append(a, "-v", v)
	}
	a = append(a, s.Image)
	return append(a, s.Cmd...)
}

// cmdRunner runs `docker args...` with stdin and returns stdout and stderr.
type cmdRunner func(ctx context.Context, stdin io.Reader, args ...string) (stdout, stderr []byte, err error)

// CLI drives the root Docker daemon through the docker CLI.
type CLI struct{ run cmdRunner }

// NewCLI talks to the root daemon's default socket: DOCKER_* variables are
// dropped from the environment so no other daemon or config is used.
func NewCLI() *CLI { return &CLI{run: execDocker} }

func execDocker(ctx context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DOCKER_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), errb.Bytes(), err
}

func (c *CLI) do(ctx context.Context, args ...string) ([]byte, error) {
	out, _, err := c.run(ctx, nil, args...)
	return out, err
}

func notFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such")
}

func (c *CLI) ImageExists(ctx context.Context, tag string) (bool, error) {
	_, err := c.do(ctx, "image", "inspect", "--format", "{{.Id}}", tag)
	if notFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (c *CLI) Build(ctx context.Context, tag string, contextTar []byte) error {
	_, _, err := c.run(ctx, bytes.NewReader(contextTar), "build", "-t", tag, "-")
	return err
}

func (c *CLI) NetworkCreate(ctx context.Context, name string, labels map[string]string) error {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := []string{"network", "create", "--driver", "bridge"}
	for _, k := range keys {
		args = append(args, "--label", k+"="+labels[k])
	}
	_, err := c.do(ctx, append(args, name)...)
	return err
}

func (c *CLI) NetworkRemove(ctx context.Context, name string) error {
	_, err := c.do(ctx, "network", "rm", name)
	return err
}

func (c *CLI) Run(ctx context.Context, spec ContainerSpec) error {
	_, err := c.do(ctx, spec.Args()...)
	return err
}

func (c *CLI) Exec(ctx context.Context, container, user string, args ...string) ([]byte, error) {
	return c.do(ctx, append([]string{"exec", "--user", user, container}, args...)...)
}

const inspectFormat = `{{.State.Running}} {{.State.ExitCode}}{{range .NetworkSettings.Networks}} {{.IPAddress}}{{end}}`

func (c *CLI) Inspect(ctx context.Context, name string) (State, error) {
	out, err := c.do(ctx, "container", "inspect", "--format", inspectFormat, name)
	if notFound(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return State{}, fmt.Errorf("docker inspect %s: unexpected output %q", name, out)
	}
	code, err := strconv.Atoi(f[1])
	if err != nil {
		return State{}, fmt.Errorf("docker inspect %s: unexpected output %q", name, out)
	}
	st := State{Exists: true, Running: f[0] == "true", ExitCode: code}
	for _, ip := range f[2:] {
		if ip != "" {
			st.IP = ip
			break
		}
	}
	return st, nil
}

func (c *CLI) Logs(ctx context.Context, name string, tail int) (string, error) {
	out, errb, err := c.run(ctx, nil, "logs", "--tail", strconv.Itoa(tail), name)
	return string(out) + string(errb), err
}

func (c *CLI) Remove(ctx context.Context, name string) error {
	_, err := c.do(ctx, "rm", "-f", name)
	return err
}

func (c *CLI) Stop(ctx context.Context, name string, timeout time.Duration) error {
	_, err := c.do(ctx, "stop", "-t", strconv.Itoa(int(timeout/time.Second)), name)
	return err
}

func (c *CLI) ListRuns(ctx context.Context) ([]string, error) {
	out, err := c.do(ctx, "ps", "-a", "--filter", "label=jauntbox.agent=1", "--format", `{{.Label "jauntbox.run"}}`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ids = append(ids, l)
		}
	}
	return ids, nil
}
