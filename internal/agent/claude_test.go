package agent

import (
	"reflect"
	"testing"
)

func TestClaudeCodeAdapter(t *testing.T) {
	var a Adapter = ClaudeCode{}
	if a.Name() != "claude-code" {
		t.Fatalf("name %q", a.Name())
	}
	if got := a.Credentials(); !reflect.DeepEqual(got, []string{"CLAUDE_CODE_OAUTH_TOKEN"}) {
		t.Fatalf("credentials %v", got)
	}
	cases := []struct {
		resume, cont, skip bool
		want               []string
	}{
		{false, false, false, []string{"claude"}},
		{false, false, true, []string{"claude", "--dangerously-skip-permissions"}},
		{true, false, false, []string{"claude", ResumePrompt}},
		{false, true, false, []string{"claude", "--continue"}},
		{true, true, false, []string{"claude", "--continue"}},
		{true, false, true, []string{"claude", "--dangerously-skip-permissions", ResumePrompt}},
		{true, true, true, []string{"claude", "--dangerously-skip-permissions", "--continue"}},
	}
	for _, c := range cases {
		if got := a.InteractiveCmd(c.resume, c.cont, c.skip); !reflect.DeepEqual(got, c.want) {
			t.Errorf("InteractiveCmd(%v,%v,%v) = %q, want %q", c.resume, c.cont, c.skip, got, c.want)
		}
	}
	if got := a.UnattendedCmd(); !reflect.DeepEqual(got, []string{"jaunt-unattended"}) {
		t.Fatalf("unattended %v", got)
	}
	if ResumePrompt != "Read CONTINUATION.md at the repository root and continue from it." {
		t.Fatal("resume prompt changed")
	}
}
