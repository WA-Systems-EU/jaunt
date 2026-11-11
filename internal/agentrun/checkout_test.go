package agentrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkCheckout makes a minimal git checkout at dir with the given origin URL
// and HEAD content.
func mkCheckout(t *testing.T, dir, url, head string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\tbare = false\n"
	if url != "" {
		cfg += "[remote \"upstream\"]\n\turl = https://github.com/other/thing\n[remote \"origin\"]\n\turl = " + url + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
}

func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFindCheckout(t *testing.T) {
	t.Run("subdirectory https", func(t *testing.T) {
		home := realTemp(t)
		root := filepath.Join(home, "src", "repo")
		mkCheckout(t, root, "https://github.com/Owner/Repo.git", "ref: refs/heads/feat/x\n")
		sub := filepath.Join(root, "a", "b")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		c, err := FindCheckout(sub, home)
		if err != nil {
			t.Fatal(err)
		}
		if c != (Checkout{Root: root, Repo: "Owner/Repo", Branch: "feat/x"}) {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("ssh and no .git suffix", func(t *testing.T) {
		home := realTemp(t)
		root := filepath.Join(home, "r")
		for _, u := range []string{"git@github.com:o/r.git", "git@github.com:o/r", "https://github.com/o/r", "\"https://github.com/o/r.git\""} {
			mkCheckout(t, root, u, "ref: refs/heads/main\n")
			c, err := FindCheckout(root, home)
			if err != nil || c.Repo != "o/r" || c.Branch != "main" || c.Detached {
				t.Fatalf("%s: %+v %v", u, c, err)
			}
		}
	})
	t.Run("non-GitHub origin", func(t *testing.T) {
		home := realTemp(t)
		root := filepath.Join(home, "r")
		mkCheckout(t, root, "https://gitlab.com/o/r.git", "ref: refs/heads/main\n")
		_, err := FindCheckout(root, home)
		if err == nil || !strings.Contains(err.Error(), "https://gitlab.com/o/r.git") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("no origin", func(t *testing.T) {
		home := realTemp(t)
		root := filepath.Join(home, "r")
		mkCheckout(t, root, "", "ref: refs/heads/main\n")
		if _, err := FindCheckout(root, home); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("dot-git file refused", func(t *testing.T) {
		home := realTemp(t)
		root := filepath.Join(home, "wt")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := FindCheckout(root, home); err == nil || !strings.Contains(err.Error(), "worktree") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("dot-git symlink refused", func(t *testing.T) {
		home := realTemp(t)
		real := filepath.Join(home, "real")
		mkCheckout(t, real, "https://github.com/o/r", "ref: refs/heads/main\n")
		root := filepath.Join(home, "link")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(real, ".git"), filepath.Join(root, ".git")); err != nil {
			t.Fatal(err)
		}
		if _, err := FindCheckout(root, home); err == nil {
			t.Fatal("symlinked .git accepted")
		}
	})
	t.Run("outside home refused", func(t *testing.T) {
		home := realTemp(t)
		other := realTemp(t)
		mkCheckout(t, other, "https://github.com/o/r", "ref: refs/heads/main\n")
		if _, err := FindCheckout(other, home); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("err %v", err)
		}
		// through a symlink inside home
		link := filepath.Join(home, "sneaky")
		if err := os.Symlink(other, link); err != nil {
			t.Fatal(err)
		}
		if _, err := FindCheckout(link, home); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("symlink err %v", err)
		}
		// home itself is not a checkout location
		mkCheckout(t, home, "https://github.com/o/r", "ref: refs/heads/main\n")
		if _, err := FindCheckout(home, home); err == nil {
			t.Fatal("home itself accepted")
		}
	})
	t.Run("not a checkout", func(t *testing.T) {
		home := realTemp(t)
		dir := filepath.Join(home, "notes")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := FindCheckout(dir, home); err == nil || !strings.Contains(err.Error(), "not inside a git checkout") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("outside home: same error whether or not it exists", func(t *testing.T) {
		home := realTemp(t)
		other := realTemp(t)
		_, e1 := FindCheckout(other, home)
		_, e2 := FindCheckout(filepath.Join(other, "nope"), home)
		_, e3 := FindCheckout(filepath.Join(home, "..", filepath.Base(other), "nope"), home)
		for _, e := range []error{e1, e2, e3} {
			if e == nil || !strings.Contains(e.Error(), "outside") || strings.Contains(e.Error(), "no such file") {
				t.Fatalf("err %v", e)
			}
		}
		// a dangling link inside home to a missing outside path
		if err := os.Symlink(filepath.Join(other, "nope"), filepath.Join(home, "dangling")); err != nil {
			t.Fatal(err)
		}
		if _, err := FindCheckout(filepath.Join(home, "dangling"), home); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("colon in path refused", func(t *testing.T) {
		home := realTemp(t)
		root := filepath.Join(home, "a:b")
		mkCheckout(t, root, "https://github.com/o/r", "ref: refs/heads/main\n")
		if _, err := FindCheckout(root, home); err == nil || !strings.Contains(err.Error(), "':'") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("detached HEAD", func(t *testing.T) {
		home := realTemp(t)
		root := filepath.Join(home, "r")
		mkCheckout(t, root, "https://github.com/o/r", "0123456789abcdef0123456789abcdef01234567\n")
		c, err := FindCheckout(root, home)
		if err != nil || !c.Detached || c.Branch != "" {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("symlinked config not followed", func(t *testing.T) {
		home := realTemp(t)
		root := filepath.Join(home, "r")
		mkCheckout(t, root, "https://github.com/o/r", "ref: refs/heads/main\n")
		secret := filepath.Join(home, "secret")
		if err := os.WriteFile(secret, []byte("[remote \"origin\"]\nurl = https://github.com/o/r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := filepath.Join(root, ".git", "config")
		os.Remove(cfg)
		if err := os.Symlink(secret, cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := FindCheckout(root, home); err == nil {
			t.Fatal("symlinked config followed")
		}
	})
}
