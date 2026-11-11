package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Everything a caller passes to a renderer goes through these two functions,
// so that output (often remote command stderr) can neither move the cursor
// nor change the number of rows a line occupies.

// cleanText strips escape sequences and trailing newlines and turns every C0
// control character except \n (and DEL) into a space.
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = ansi.Strip(s)
	s = strings.TrimRight(s, "\r\n")
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n') || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// cleanLine sanitises a single-line field: newlines become spaces.
func cleanLine(s string) string {
	return strings.ReplaceAll(cleanText(s), "\n", " ")
}

// cleanLines sanitises multi-line fields and splits them into lines; empty
// messages are dropped.
func cleanLines(msgs ...string) []string {
	var out []string
	for _, m := range msgs {
		m = cleanText(m)
		if m == "" {
			continue
		}
		out = append(out, strings.Split(m, "\n")...)
	}
	return out
}
