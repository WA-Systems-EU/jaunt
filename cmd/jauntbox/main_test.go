package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRequireEnvFallsBackToEnvFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(f, []byte("export JB_TEST_TOKEN='from-file'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JB_TEST_TOKEN", "")
	if v, err := requireEnv("JB_TEST_TOKEN", "x", f); err != nil || v != "from-file" {
		t.Fatalf("got %q, %v", v, err)
	}
	t.Setenv("JB_TEST_TOKEN", "from-env")
	if v, _ := requireEnv("JB_TEST_TOKEN", "x", f); v != "from-env" {
		t.Fatalf("env should win, got %q", v)
	}
	t.Setenv("JB_TEST_TOKEN", "")
	if _, err := requireEnv("JB_TEST_TOKEN", "x", filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("expected error")
	}
}
