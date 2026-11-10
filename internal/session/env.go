package session

import (
	"os"
	"strings"
)

// herdrPaneEnv lists the variables herdr injects into every pane it spawns
// (its pane environment). They mark "this shell is inside a herdr", which makes
// a herdr started there refuse as nested. User settings that herdr merely reads
// (HERDR_CONFIG_PATH, HERDR_LOG, HERDR_REMOTE_BINARY, ...) are deliberately not
// listed so they still reach the child.
var herdrPaneEnv = map[string]bool{
	"HERDR_SOCKET_PATH":        true,
	"HERDR_CLIENT_SOCKET_PATH": true,
	"HERDR_SESSION":            true,
	"HERDR_BIN_PATH":           true,
	"HERDR_ENV":                true,
	"HERDR_WORKSPACE_ID":       true,
	"HERDR_TAB_ID":             true,
	"HERDR_PANE_ID":            true,
	"HERDR_STARTUP_CWD":        true,
}

// WithoutHerdrEnv returns env minus the variables herdr injects into a pane.
// The attach targets the box's session, not the local one, so these must not
// leak into the child.
func WithoutHerdrEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !herdrPaneEnv[name] {
			out = append(out, kv)
		}
	}
	return out
}

// InsideHerdr reports whether this process runs in a local herdr pane.
func InsideHerdr() bool {
	return os.Getenv("HERDR_ENV") != "" || os.Getenv("HERDR_SESSION") != ""
}
