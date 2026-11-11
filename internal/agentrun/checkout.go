package agentrun

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// Checkout is the git checkout an agent run works in.
type Checkout struct {
	Root     string // symlink-free path of the working tree
	Repo     string // owner/repo from the origin remote
	Branch   string // current branch; empty when Detached
	Detached bool
}

// FindCheckout walks up from dir to the first directory holding .git and
// validates it for an agent run: .git is a real directory (not a linked
// worktree or submodule file, not a symlink), the symlink-free root lies
// strictly inside home, and origin points at a GitHub repository. It only
// parses files; it never runs git.
func FindCheckout(dir, home string) (Checkout, error) {
	var c Checkout
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return c, fmt.Errorf("home %s: %w", home, err)
	}
	start, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return c, err
	}
	root := start
	for {
		fi, err := os.Lstat(filepath.Join(root, ".git"))
		if err == nil {
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				return c, fmt.Errorf("%s/.git is a symlink; agents need a plain checkout", root)
			case fi.Mode().IsRegular():
				return c, fmt.Errorf("%s/.git is a file (a linked worktree or a submodule); run the agent in the main checkout", root)
			case !fi.IsDir():
				return c, fmt.Errorf("%s/.git is not a directory", root)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return c, err
		}
		parent := filepath.Dir(root)
		if parent == root {
			return c, fmt.Errorf("%s is not inside a git checkout", dir)
		}
		root = parent
	}
	if !strings.HasPrefix(root, realHome+string(filepath.Separator)) {
		return c, fmt.Errorf("the checkout %s is outside %s; agents only run in checkouts under your home", root, home)
	}
	c.Root = root

	cfg, err := readNoFollow(filepath.Join(root, ".git", "config"))
	if err != nil {
		return c, err
	}
	url, ok := originURL(cfg)
	if !ok {
		return c, fmt.Errorf("%s has no origin remote; the agent needs a GitHub origin", root)
	}
	if c.Repo, err = parseGitHubURL(url); err != nil {
		return c, err
	}

	head, err := readNoFollow(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return c, err
	}
	h := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(h, "ref: "); ok {
		b, ok := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/")
		if !ok || b == "" {
			return c, fmt.Errorf("%s/.git/HEAD points at %q, not a branch", root, ref)
		}
		c.Branch = b
	} else {
		c.Detached = true
	}
	return c, nil
}

const maxGitFile = 1 << 20

// readNoFollow reads a small regular file, refusing a symlink as the last
// component and anything that is not a regular file (a FIFO would block).
func readNoFollow(path string) ([]byte, error) {
	b, _, err := readNoFollowOwner(path)
	return b, err
}

// readNoFollowOwner is readNoFollow that also returns the file's owner uid.
func readNoFollowOwner(path string) ([]byte, int, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, -1, fmt.Errorf("%s is a symlink; refusing to follow it", path)
	}
	if err != nil {
		return nil, -1, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, -1, err
	}
	if !fi.Mode().IsRegular() {
		return nil, -1, fmt.Errorf("%s is not a regular file", path)
	}
	uid := int(fi.Sys().(*syscall.Stat_t).Uid)
	b, err := io.ReadAll(io.LimitReader(f, maxGitFile+1))
	if err != nil {
		return nil, -1, err
	}
	if len(b) > maxGitFile {
		return nil, -1, fmt.Errorf("%s is too large", path)
	}
	return b, uid, nil
}

var sectionRE = regexp.MustCompile(`^\[\s*remote\s+"origin"\s*\]$`)

// originURL returns the first url of [remote "origin"] in a git config.
func originURL(cfg []byte) (string, bool) {
	in := false
	sc := bufio.NewScanner(bytes.NewReader(cfg))
	sc.Buffer(make([]byte, 64*1024), maxGitFile)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		if l[0] == '[' {
			in = sectionRE.MatchString(l)
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "url") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		return v, true
	}
	return "", false
}

var (
	ghURLRE    = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)
	repoPartRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// parseGitHubURL turns a GitHub https or ssh remote URL into owner/repo.
func parseGitHubURL(url string) (string, error) {
	m := ghURLRE.FindStringSubmatch(url)
	if m == nil || !safePart(m[1]) || !safePart(m[2]) {
		return "", fmt.Errorf("origin %s is not a GitHub repository URL (want https://github.com/owner/repo or git@github.com:owner/repo)", url)
	}
	return m[1] + "/" + m[2], nil
}

func safePart(s string) bool { return repoPartRE.MatchString(s) && s != "." && s != ".." }
