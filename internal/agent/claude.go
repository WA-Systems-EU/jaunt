package agent

// ClaudeCode is the Claude Code CLI.
type ClaudeCode struct{}

func (ClaudeCode) Name() string { return "claude-code" }

func (ClaudeCode) Credentials() []string { return []string{"CLAUDE_CODE_OAUTH_TOKEN"} }

func (ClaudeCode) InteractiveCmd(resume, continueSession, skipPermissions bool) []string {
	argv := []string{"claude"}
	if skipPermissions {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	switch {
	case continueSession:
		argv = append(argv, "--continue")
	case resume:
		argv = append(argv, ResumePrompt)
	}
	return argv
}

// UnattendedCmd runs the in-image wrapper, which reads JAUNT_PROMPT.
func (ClaudeCode) UnattendedCmd() []string { return []string{"jaunt-unattended"} }
