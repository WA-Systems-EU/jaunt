// Package agent describes the coding agents jauntbox can run in a container.
package agent

// Adapter is one coding agent: how to run it and which credentials it needs.
type Adapter interface {
	// Name identifies the agent.
	Name() string
	// Credentials lists the agents.env keys (in the box vault) the agent
	// needs, passed to the container as environment variables of that name.
	Credentials() []string
	// InteractiveCmd is the argv run through `docker exec -it`. continueSession
	// wins over resume; resume passes ResumePrompt as the opening prompt.
	InteractiveCmd(resume, continueSession, skipPermissions bool) []string
	// UnattendedCmd is the container command of an unattended run.
	UnattendedCmd() []string
}

// ResumePrompt opens a session on a branch that already has CONTINUATION.md.
const ResumePrompt = "Read CONTINUATION.md at the repository root and continue from it."
