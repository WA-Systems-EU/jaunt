package session

import (
	"reflect"
	"testing"
)

func TestWithoutHerdrEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HERDR_ENV=1", "HOME=/home/x", "HERDR_SESSION=s",
		"HERDR_SOCKET_PATH=/tmp/s", "HERDR_PANE_ID=p", "HERDR_TAB_ID=t", "HERDR_WORKSPACE_ID=w",
		"SSH_AUTH_SOCK=/tmp/agent", "TERM=xterm", "MY_HERDR=1", "herdr_lower=1", "HERDR=bare",
		"HERDR_STARTUP_CWD=/x", "HERDR_CLIENT_SOCKET_PATH=/c", "HERDR_BIN_PATH=/b",
		"HERDR_CONFIG_PATH=/cfg", "HERDR_REMOTE_BINARY=/rb", "HERDR_LOG=debug",
	}
	want := []string{"PATH=/usr/bin", "HOME=/home/x", "SSH_AUTH_SOCK=/tmp/agent", "TERM=xterm", "MY_HERDR=1", "herdr_lower=1", "HERDR=bare",
		"HERDR_CONFIG_PATH=/cfg", "HERDR_REMOTE_BINARY=/rb", "HERDR_LOG=debug"}
	if got := WithoutHerdrEnv(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestInsideHerdr(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_SESSION", "")
	if InsideHerdr() {
		t.Fatal("empty vars should not count")
	}
	t.Setenv("HERDR_SESSION", "x")
	if !InsideHerdr() {
		t.Fatal("HERDR_SESSION should count")
	}
}
