// Package ui reports the progress of long commands as a list of steps.
package ui

import (
	"fmt"
	"time"
)

// Steps reports a command's progress. Implementations: plain lines, a live
// animated block, a recorder for tests, and Nop.
type Steps interface {
	Header(title, context string)
	Plan(labels ...string)
	Start(label string) Step
	Note(line string)
	End(ok bool, summary string, lines ...string)
	Close()
}

// Step is one running step. It reports at most one outcome (Done, Skip or
// Fail); later outcome calls are ignored.
type Step interface {
	Detail(s string)
	Warn(msg string)
	Done()
	Skip(reason string)
	Fail(err error, hint string)
}

// FormatDuration renders a step duration: 0.2s, 8.1s, 41s, 1m12s.
func FormatDuration(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		s := int(d.Seconds())
		return fmt.Sprintf("%dm%ds", s/60, s%60)
	}
}

type nop struct{}
type nopStep struct{}

// Nop returns a Steps that prints nothing.
func Nop() Steps { return nop{} }

func (nop) Header(string, string)       {}
func (nop) Plan(...string)              {}
func (nop) Start(string) Step           { return nopStep{} }
func (nop) Note(string)                 {}
func (nop) End(bool, string, ...string) {}
func (nop) Close()                      {}

func (nopStep) Detail(string)      {}
func (nopStep) Warn(string)        {}
func (nopStep) Done()              {}
func (nopStep) Skip(string)        {}
func (nopStep) Fail(error, string) {}
