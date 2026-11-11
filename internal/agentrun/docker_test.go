package agentrun

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recRunner struct {
	calls [][]string
	stdin []byte
	out   string
	err   error
}

func (r *recRunner) run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	r.calls = append(r.calls, args)
	if stdin != nil {
		r.stdin, _ = io.ReadAll(stdin)
	}
	return []byte(r.out), nil, r.err
}

func TestDockerCLIArgs(t *testing.T) {
	ctx := context.Background()
	r := &recRunner{}
	c := &CLI{run: r.run}
	spec := ContainerSpec{Name: "jauntbox-x", Hostname: "x", Network: "jauntbox-x", UID: 1000, GID: 1000,
		Labels: []string{"jauntbox.agent=1"}, Image: "img", Cmd: []string{"jaunt-unattended"}}

	_ = c.Build(ctx, "jauntbox/agent:abc", []byte("TAR"))
	_ = c.NetworkCreate(ctx, "jauntbox-x", map[string]string{"b": "2", "a": "1"})
	_ = c.NetworkRemove(ctx, "jauntbox-x")
	_ = c.Run(ctx, spec)
	_, _ = c.Exec(ctx, "jauntbox-x", "1000:1000", "test", "-f", "/tmp/jauntbox-ready")
	_, _ = c.Logs(ctx, "jauntbox-x", 50)
	_ = c.Remove(ctx, "jauntbox-x")
	_ = c.Stop(ctx, "jauntbox-x", 5*time.Minute)
	want := [][]string{
		{"build", "-t", "jauntbox/agent:abc", "-"},
		{"network", "create", "--driver", "bridge", "--label", "a=1", "--label", "b=2", "jauntbox-x"},
		{"network", "rm", "jauntbox-x"},
		spec.Args(),
		{"exec", "--user", "1000:1000", "jauntbox-x", "test", "-f", "/tmp/jauntbox-ready"},
		{"logs", "--tail", "50", "jauntbox-x"},
		{"rm", "-f", "jauntbox-x"},
		{"stop", "-t", "300", "jauntbox-x"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls\n got %q\nwant %q", r.calls, want)
	}
	if string(r.stdin) != "TAR" {
		t.Fatalf("stdin %q", r.stdin)
	}
	if a := spec.Args(); a[len(a)-2] != "img" || a[len(a)-1] != "jaunt-unattended" {
		t.Fatalf("args %q", a)
	}
}

func TestDockerCLIImageExists(t *testing.T) {
	ctx := context.Background()
	r := &recRunner{out: "sha256:1\n"}
	c := &CLI{run: r.run}
	if ok, err := c.ImageExists(ctx, "t"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if !reflect.DeepEqual(r.calls[0], []string{"image", "inspect", "--format", "{{.Id}}", "t"}) {
		t.Fatalf("%q", r.calls[0])
	}
	r.err = errors.New("docker image: exit status 1: Error: No such image: t")
	if ok, err := c.ImageExists(ctx, "t"); ok || err != nil {
		t.Fatal(ok, err)
	}
	r.err = errors.New("docker image: cannot connect to the daemon")
	if _, err := c.ImageExists(ctx, "t"); err == nil {
		t.Fatal("daemon error swallowed")
	}
}

func TestDockerCLIInspectAndList(t *testing.T) {
	ctx := context.Background()
	r := &recRunner{out: "true 0 172.18.0.2\n"}
	c := &CLI{run: r.run}
	st, err := c.Inspect(ctx, "jauntbox-x")
	if err != nil || st != (State{Exists: true, Running: true, IP: "172.18.0.2"}) {
		t.Fatal(st, err)
	}
	if !reflect.DeepEqual(r.calls[0], []string{"container", "inspect", "--format", inspectFormat, "jauntbox-x"}) {
		t.Fatalf("%q", r.calls[0])
	}
	r.out = "false 137\n"
	if st, err := c.Inspect(ctx, "x"); err != nil || st != (State{Exists: true, ExitCode: 137}) {
		t.Fatal(st, err)
	}
	r.out, r.err = "", errors.New("Error: No such container: x")
	if st, err := c.Inspect(ctx, "x"); err != nil || st.Exists {
		t.Fatal(st, err)
	}
	r.out, r.err = "a-1\n\nb-2\n", nil
	ids, err := c.ListRuns(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{"a-1", "b-2"}) {
		t.Fatal(ids, err)
	}
	if got := strings.Join(r.calls[len(r.calls)-1], " "); got != `ps -a --filter label=jauntbox.agent=1 --format {{.Label "jauntbox.run"}}` {
		t.Fatalf("%s", got)
	}
}
