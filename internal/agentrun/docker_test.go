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
	calls  [][]string
	stdin  []byte
	out    string
	stderr string
	err    error
}

func (r *recRunner) run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	r.calls = append(r.calls, args)
	if stdin != nil {
		r.stdin, _ = io.ReadAll(stdin)
	}
	return []byte(r.out), []byte(r.stderr), r.err
}

func TestDockerCLIArgs(t *testing.T) {
	ctx := context.Background()
	r := &recRunner{}
	c := &CLI{run: r.run}
	spec := ContainerSpec{Name: "jauntbox-x", Hostname: "x", Network: "jauntbox-x", UID: 1000, GID: 1000,
		Labels: []string{"jauntbox.agent=1"}, Image: "img", Cmd: []string{"jaunt-unattended"}}

	_ = c.Build(ctx, "jauntbox/agent:abc", []byte("TAR"))
	_ = c.NetworkCreate(ctx, "jauntbox-x", "br-jb0123456789", map[string]string{"b": "2", "a": "1"})
	_ = c.NetworkRemove(ctx, "jauntbox-x")
	_ = c.Run(ctx, spec)
	_, _ = c.Exec(ctx, "jauntbox-x", "1000:1000", "test", "-f", "/tmp/jauntbox-ready")
	_, _ = c.Exec(ctx, "jauntbox-x", "", "jaunt-wrapup-fallback", "x")
	_, _ = c.Logs(ctx, "jauntbox-x", 50)
	_ = c.Remove(ctx, "jauntbox-x")
	_ = c.Stop(ctx, "jauntbox-x", 5*time.Minute)
	want := [][]string{
		{"build", "-t", "jauntbox/agent:abc", "-"},
		{"network", "create", "--driver", "bridge", "-o", "com.docker.network.bridge.name=br-jb0123456789", "--label", "a=1", "--label", "b=2", "jauntbox-x"},
		{"network", "rm", "jauntbox-x"},
		spec.Args(),
		{"exec", "--user", "1000:1000", "jauntbox-x", "test", "-f", "/tmp/jauntbox-ready"},
		{"exec", "jauntbox-x", "jaunt-wrapup-fallback", "x"},
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
	r.err, r.stderr = errors.New("exit status 1"), "Error response from daemon: No such image: t\n"
	if ok, err := c.ImageExists(ctx, "t"); ok || err != nil {
		t.Fatal(ok, err)
	}
	r.err, r.stderr = errors.New("docker image: cannot connect to the daemon"), "Cannot connect to the Docker daemon"
	if _, err := c.ImageExists(ctx, "t"); err == nil {
		t.Fatal("daemon error swallowed")
	}
	// a missing socket says "no such file or directory": not "image absent"
	sock := "failed to connect to the docker API at unix:///var/run/docker.sock: dial unix /var/run/docker.sock: connect: no such file or directory"
	r.err, r.stderr = errors.New("docker image: exit status 1: "+sock), sock
	if _, err := c.ImageExists(ctx, "t"); err == nil {
		t.Fatal("connect error read as image absent")
	}
	if _, err := c.Inspect(ctx, "x"); err == nil {
		t.Fatal("connect error read as container absent")
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
	r.out, r.err, r.stderr = "", errors.New("exit status 1"), "Error: No such container: x\n"
	if st, err := c.Inspect(ctx, "x"); err != nil || st.Exists {
		t.Fatal(st, err)
	}
	r.out, r.err, r.stderr = "a-1\n\nb-2\n", nil, ""
	ids, err := c.ListRuns(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{"a-1", "b-2"}) {
		t.Fatal(ids, err)
	}
	if got := strings.Join(r.calls[len(r.calls)-1], " "); got != `ps -a --filter label=jauntbox.agent=1 --format {{.Label "jauntbox.run"}}` {
		t.Fatalf("%s", got)
	}
}

func TestNetworkCreateArgs(t *testing.T) {
	r := &recRunner{}
	c := &CLI{run: r.run}
	if err := c.NetworkCreate(context.Background(), "jauntbox-x", "br-jbabcdef0123", map[string]string{"b": "2", "a": "1"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"network", "create", "--driver", "bridge", "-o", "com.docker.network.bridge.name=br-jbabcdef0123",
		"--label", "a=1", "--label", "b=2", "jauntbox-x"}
	if len(r.calls) != 1 || !reflect.DeepEqual(r.calls[0], want) {
		t.Fatalf("got %q\nwant %q", r.calls, want)
	}
	for _, a := range r.calls[0] {
		if a == "--ipv6" {
			t.Fatal("--ipv6 passed")
		}
	}
}

func TestContainerSpecAddHost(t *testing.T) {
	s := ContainerSpec{Name: "n", Hostname: "h", Network: "n", UID: 1, GID: 1, Image: "img",
		ExtraHosts: []string{"host.jauntbox:host-gateway"}, Labels: []string{"a=b"}}
	a := s.Args()
	i := indexOf(a, "--pids-limit")
	if i < 0 || a[i+1] != "4096" || a[i+2] != "--add-host" || a[i+3] != "host.jauntbox:host-gateway" || a[i+4] != "--label" {
		t.Fatalf("args %q", a)
	}
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}
