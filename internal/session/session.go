// Package session adapts a persistent terminal-session tool (herdr) to the
// jauntbox client: how to attach to it and how to prepare its workspaces.
package session

// Session is the persistent terminal session living on the box.
type Session interface {
	// AttachCommand is the command the client execs to attach; localHerdr
	// reports whether the session tool is on the client's PATH.
	AttachCommand(host, user, name string, localHerdr bool) []string
	// StartCommand is one remote command line that makes sure the named
	// session's server is running (starting it detached if not).
	StartCommand(name string) []string
	// ListCommand is the remote command line whose output lists the
	// session's workspaces, in the format EnsureWorkspaces parses.
	ListCommand(name string) []string
	// EnsureWorkspaces returns the remote command lines that create one
	// workspace per project dir under /home/<user>/src that listOutput (the
	// output of ListCommand) does not already show.
	EnsureWorkspaces(name, user string, projects []string, listOutput []byte) ([][]string, error)
}

// Name is the herdr session name every jauntbox client attaches to.
const Name = "jauntbox"
