package agentimage

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestTagStableAndContentAddressed(t *testing.T) {
	a, err := tagOf(fstest.MapFS{"Dockerfile": {Data: []byte("FROM x")}, "jaunt-hook": {Data: []byte("one")}})
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := tagOf(fstest.MapFS{"Dockerfile": {Data: []byte("FROM x")}, "jaunt-hook": {Data: []byte("one")}})
	b, _ := tagOf(fstest.MapFS{"Dockerfile": {Data: []byte("FROM x")}, "jaunt-hook": {Data: []byte("two")}})
	if a != a2 {
		t.Errorf("tag not stable: %s vs %s", a, a2)
	}
	if a == b {
		t.Error("changing a script must change the tag")
	}
	if !regexp.MustCompile(`^jauntbox/agent:[0-9a-f]{12}$`).MatchString(Tag()) {
		t.Errorf("Tag() = %q", Tag())
	}
}

func TestContextHasDockerfileAndExecutableScripts(t *testing.T) {
	b, err := Context()
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]int64{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[h.Name] = h.Mode
		if _, err := io.Copy(io.Discard, tr); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := modes["Dockerfile"]; !ok {
		t.Fatalf("no Dockerfile at root: %v", modes)
	}
	for _, s := range []string{"jaunt-agent-init", "jaunt-git-credential", "jaunt-hook", "jaunt-unattended", "jaunt-wrapup-fallback"} {
		if modes[s] != 0o755 {
			t.Errorf("%s mode = %o, want 755", s, modes[s])
		}
	}
	if _, ok := modes["managed-settings.json"]; !ok {
		t.Error("managed-settings.json missing")
	}
	for n := range modes {
		if strings.HasSuffix(n, "_test.sh") {
			t.Errorf("test file %s must not be in the context", n)
		}
	}
}

type fakeDocker struct {
	exists bool
	builds []string
}

func (f *fakeDocker) ImageExists(ctx context.Context, tag string) (bool, error) {
	return f.exists, nil
}
func (f *fakeDocker) Build(ctx context.Context, tag string, tarball []byte) error {
	f.builds = append(f.builds, tag)
	return nil
}

func TestEnsureBuildsOnlyWhenMissing(t *testing.T) {
	f := &fakeDocker{exists: true}
	if err := Ensure(context.Background(), f); err != nil || len(f.builds) != 0 {
		t.Fatalf("exists: err=%v builds=%v", err, f.builds)
	}
	f = &fakeDocker{}
	if err := Ensure(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(f.builds) != 1 || f.builds[0] != Tag() {
		t.Fatalf("builds = %v, want [%s]", f.builds, Tag())
	}
}
