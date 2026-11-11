// Package agentimage builds the agent container image from the embedded build context.
package agentimage

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/WA-Systems-EU/jaunt/provision"
)

// Docker is the subset of the Docker CLI the image needs.
type Docker interface {
	ImageExists(ctx context.Context, tag string) (bool, error)
	Build(ctx context.Context, tag string, contextTar []byte) error
}

func embedded() (fs.FS, error) { return fs.Sub(provision.FS, "agent") }

// files lists the build-context files (no tests), sorted.
func files(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), "_test.sh") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func tagOf(fsys fs.FS) (string, error) {
	names, err := files(fsys)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(b))
		h.Write(b)
	}
	return "jauntbox-agent:" + hex.EncodeToString(h.Sum(nil))[:12], nil
}

// Tag is the content-addressed image tag, e.g. jauntbox-agent:0123456789ab.
func Tag() string {
	fsys, err := embedded()
	if err != nil {
		panic(err)
	}
	t, err := tagOf(fsys)
	if err != nil {
		panic(err)
	}
	return t
}

// Context returns a tar of the build context with Dockerfile at the root; scripts are mode 0755.
func Context() ([]byte, error) {
	fsys, err := embedded()
	if err != nil {
		return nil, err
	}
	names, err := files(fsys)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		mode := int64(0o644)
		if strings.HasPrefix(n, "jaunt-") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: mode, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Ensure builds the image unless its tag already exists.
func Ensure(ctx context.Context, d Docker) error {
	tag := Tag()
	ok, err := d.ImageExists(ctx, tag)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	tarball, err := Context()
	if err != nil {
		return err
	}
	return d.Build(ctx, tag, tarball)
}
