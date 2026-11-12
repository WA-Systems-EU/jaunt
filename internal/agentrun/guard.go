package agentrun

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openGitDir opens <root>/.git by walking down from home one component at a
// time without following symlinks, so a path swapped for a link after
// FindCheckout cannot redirect root's writes.
func openGitDir(home, root string) (int, error) {
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return -1, err
	}
	rel, err := filepath.Rel(realHome, root)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return -1, fmt.Errorf("the checkout %s is outside %s", root, home)
	}
	fd, err := unix.Open(realHome, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: realHome, Err: err}
	}
	cur := realHome
	for _, c := range append(strings.Split(rel, string(filepath.Separator)), ".git") {
		cur = filepath.Join(cur, c)
		nfd, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return -1, fmt.Errorf("%s is a symlink or not a directory; refusing", cur)
		}
		if err != nil {
			return -1, &os.PathError{Op: "open", Path: cur, Err: err}
		}
		fd = nfd
	}
	return fd, nil
}

func lstatAt(dirfd int, name string) (unix.Stat_t, bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	return st, true, nil
}

func isType(st unix.Stat_t, t uint32) bool { return uint32(st.Mode)&unix.S_IFMT == t }

// guardFiles checks the files the container gets read-only over the
// checkout: .git/config (a regular file), .git/hooks and .git/modules
// (directories when present), .git/commondir (a regular file holding "."
// when present). None may be a symlink. With create, a missing hooks dir and
// commondir are created (O_EXCL, never through a link) and given to uid:gid.
// It reports whether .git/modules exists.
func guardFiles(home, root string, create bool, uid, gid int) (bool, error) {
	gfd, err := openGitDir(home, root)
	if err != nil {
		return false, err
	}
	defer unix.Close(gfd)
	g := filepath.Join(root, ".git")
	symlink := func(name string) error {
		return fmt.Errorf("%s is a symlink; agents need a plain checkout: replace it with a real %s", filepath.Join(g, name), name)
	}

	st, ok, err := lstatAt(gfd, "config")
	switch {
	case err != nil:
		return false, err
	case !ok:
		return false, fmt.Errorf("%s is missing", filepath.Join(g, "config"))
	case isType(st, unix.S_IFLNK):
		return false, symlink("config")
	case !isType(st, unix.S_IFREG):
		return false, fmt.Errorf("%s is not a regular file", filepath.Join(g, "config"))
	}

	hasModules := false
	if st, ok, err := lstatAt(gfd, "modules"); err != nil {
		return false, err
	} else if ok {
		if isType(st, unix.S_IFLNK) {
			return false, symlink("modules")
		}
		if !isType(st, unix.S_IFDIR) {
			return false, fmt.Errorf("%s is not a directory", filepath.Join(g, "modules"))
		}
		hasModules = true
	}

	if err := guardHooks(gfd, g, create, uid, gid, symlink); err != nil {
		return false, err
	}
	if err := guardCommondir(gfd, g, create, uid, gid, symlink); err != nil {
		return false, err
	}
	return hasModules, nil
}

func guardHooks(gfd int, g string, create bool, uid, gid int, symlink func(string) error) error {
	p := filepath.Join(g, "hooks")
	st, ok, err := lstatAt(gfd, "hooks")
	if err != nil {
		return err
	}
	if ok {
		if isType(st, unix.S_IFLNK) {
			return symlink("hooks")
		}
		if !isType(st, unix.S_IFDIR) {
			return fmt.Errorf("%s is not a directory", p)
		}
		return nil
	}
	if !create {
		return nil
	}
	if err := unix.Mkdirat(gfd, "hooks", 0o755); err != nil {
		return &os.PathError{Op: "mkdir", Path: p, Err: err}
	}
	fd, err := unix.Openat(gfd, "hooks", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: p, Err: err}
	}
	defer unix.Close(fd)
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return &os.PathError{Op: "chown", Path: p, Err: err}
	}
	if err := unix.Fchmod(fd, 0o755); err != nil {
		return &os.PathError{Op: "chmod", Path: p, Err: err}
	}
	return nil
}

func guardCommondir(gfd int, g string, create bool, uid, gid int, symlink func(string) error) error {
	p := filepath.Join(g, "commondir")
	st, ok, err := lstatAt(gfd, "commondir")
	if err != nil {
		return err
	}
	if ok {
		if isType(st, unix.S_IFLNK) {
			return symlink("commondir")
		}
		if !isType(st, unix.S_IFREG) {
			return fmt.Errorf("%s is not a regular file; remove it", p)
		}
		fd, err := unix.Openat(gfd, "commondir", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return &os.PathError{Op: "open", Path: p, Err: err}
		}
		f := os.NewFile(uintptr(fd), p)
		b, err := io.ReadAll(io.LimitReader(f, 4096))
		f.Close()
		if err != nil {
			return err
		}
		if c := strings.TrimRight(string(b), "\r\n"); c != "." {
			return fmt.Errorf("%s exists with the content %q (it points git at another repository); remove it and retry", p, c)
		}
		return nil
	}
	if !create {
		return nil
	}
	fd, err := unix.Openat(gfd, "commondir", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return &os.PathError{Op: "create", Path: p, Err: err}
	}
	f := os.NewFile(uintptr(fd), p)
	if err := setFile(f, []byte("."), 0o644, uid, gid); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ensureGuardFiles creates the missing guard files as described in guardFiles.
func ensureGuardFiles(home, root string, uid, gid int) error {
	_, err := guardFiles(home, root, true, uid, gid)
	return err
}
