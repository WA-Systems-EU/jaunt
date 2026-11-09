package session

import (
	"reflect"
	"testing"
)

func TestHerdrAttachCommand(t *testing.T) {
	h := Herdr{}
	got := h.AttachCommand("jauntbox-alice", "alice", "jauntbox", true)
	want := []string{"herdr", "--remote", "alice@jauntbox-alice", "--session", "jauntbox"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("local: got %v want %v", got, want)
	}
	got = h.AttachCommand("jauntbox-alice", "alice", "jauntbox", false)
	want = []string{"ssh", "-t", "alice@jauntbox-alice", "--", "herdr", "--session", "jauntbox"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ssh: got %v want %v", got, want)
	}
}

func TestEnsureWorkspacesSkipsExisting(t *testing.T) {
	list := []byte(`{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[{"label":"a","workspace_id":"w1"}]}}`)
	got, err := Herdr{}.EnsureWorkspaces("jauntbox", "alice", []string{"a", "b"}, list)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"env", "HERDR_SESSION=jauntbox", "herdr", "workspace", "create",
		"--cwd", "/home/alice/src/b", "--label", "b", "--no-focus"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestEnsureWorkspacesRejectsGarbageListing(t *testing.T) {
	if _, err := (Herdr{}).EnsureWorkspaces("jauntbox", "alice", []string{"a"}, []byte("Error: nope")); err == nil {
		t.Fatal("expected a parse error so we never create duplicates blindly")
	}
}
