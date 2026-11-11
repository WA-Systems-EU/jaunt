package ui

import (
	"io"
	"os"
)

// Live reports whether the animated renderer should be used for w: it must be
// a terminal, and neither --plain, NO_COLOR nor TERM=dumb may be in effect.
func Live(w io.Writer, plainFlag bool, getenv func(string) string, isTerminal func(fd int) bool) bool {
	if plainFlag || getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isTerminal(int(f.Fd()))
}
