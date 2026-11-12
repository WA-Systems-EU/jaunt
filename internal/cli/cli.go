// Package cli holds what the jauntbox (laptop) and jauntctl (box) commands
// share.
package cli

import (
	"errors"
	"strings"
	"time"
)

// LaptopCommands are the visible commands of the jauntbox root. jauntctl has
// no laptop tree, so it names these to point a laptop command typed on the box
// at the laptop. A cmd/jauntbox test keeps the list in step with the client.
var LaptopCommands = []string{
	"acl-snippet", "down", "init", "night", "resize", "secrets", "setup",
	"ssh", "status", "unlock", "up",
}

// ErrReported makes a command exit 1 without printing: the error is already
// on the terminal.
var ErrReported = errors.New("already reported")

// ShortDuration prints 8h0m0s as 8h and 1h30m0s as 1h30m.
func ShortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
