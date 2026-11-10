package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestRunInheritedDropsHerdrEnv(t *testing.T) {
	t.Setenv("HERDR_SESSION", "local")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("JB_KEEP", "yes")
	t.Setenv("HERDR_CONFIG_PATH", "/my/config.toml")
	out := filepath.Join(t.TempDir(), "env.txt")
	code, err := runInherited([]string{"sh", "-c", "env > " + out})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	b, _ := os.ReadFile(out)
	s := string(b)
	if strings.Contains(s, "HERDR_SESSION") || strings.Contains(s, "HERDR_ENV") {
		t.Errorf("HERDR_* leaked to child:\n%s", s)
	}
	if !strings.Contains(s, "HERDR_CONFIG_PATH=/my/config.toml") {
		t.Errorf("user herdr settings dropped:\n%s", s)
	}
	if !strings.Contains(s, "JB_KEEP=yes") {
		t.Errorf("other vars dropped:\n%s", s)
	}
}

func TestRootHasNestedFlag(t *testing.T) {
	f := newRootCmd().Flags().Lookup("nested")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("--nested flag missing or defaults on: %v", f)
	}
}
