package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type plain struct {
	mu     sync.Mutex
	w      io.Writer
	now    func() time.Time
	open   []*plainStep // started, not yet finished, in start order
	closed bool
}

// NewPlain returns a renderer that prints one line per finished step, with no
// colour and no animation.
func NewPlain(w io.Writer, now func() time.Time) Steps {
	if now == nil {
		now = time.Now
	}
	return &plain{w: w, now: now}
}

func (p *plain) printf(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.w, format, a...)
}

func (p *plain) Header(title, context string) {
	title, context = cleanLine(title), cleanLine(context)
	if context == "" {
		p.printf("%s\n", title)
		return
	}
	p.printf("%s · %s\n", title, context)
}

func (p *plain) Plan(...string) {}

func (p *plain) Note(line string) {
	for _, ln := range cleanLines(line) {
		p.printf("%s\n", ln)
	}
}

func (p *plain) Start(label string) Step {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := &plainStep{p: p, label: cleanLine(label), start: p.now()}
	p.open = append(p.open, s)
	return s
}

func (p *plain) End(ok bool, summary string, lines ...string) {
	var b strings.Builder
	if ok {
		fmt.Fprintf(&b, "● %s\n", cleanLine(summary))
	}
	for _, l := range cleanLines(lines...) {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	p.printf("%s", b.String())
}

// Close prints every step still running as interrupted, in start order, and
// marks it finished so a later outcome call is ignored.
func (p *plain) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	pending := append([]*plainStep(nil), p.open...) // writeLocked edits p.open
	for _, s := range pending {
		s.writeLocked("–", true, "interrupted")
	}
	p.open = nil
}

type plainStep struct {
	p      *plain
	label  string
	start  time.Time
	detail string
	warns  []string
	done   bool
}

func (s *plainStep) Detail(d string) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.detail = cleanLine(d)
}

func (s *plainStep) Warn(msg string) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.warns = append(s.warns, msg)
}

// writeLocked prints the step's outcome; the caller holds p.mu.
func (s *plainStep) writeLocked(sym string, withDuration bool, extra string, msgs ...string) {
	if s.done {
		return
	}
	s.done = true
	for i, o := range s.p.open {
		if o == s {
			s.p.open = append(s.p.open[:i], s.p.open[i+1:]...)
			break
		}
	}
	var b strings.Builder
	b.WriteString(sym + " " + s.label)
	if withDuration {
		b.WriteString(" " + FormatDuration(s.p.now().Sub(s.start)))
	}
	if extra != "" {
		b.WriteString(" " + extra)
	}
	b.WriteString("\n")
	for _, m := range cleanLines(msgs...) {
		fmt.Fprintf(&b, "  %s\n", m)
	}
	fmt.Fprint(s.p.w, b.String())
}

func (s *plainStep) Done() {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if len(s.warns) > 0 {
		s.writeLocked("!", true, s.detail, s.warns...)
		return
	}
	s.writeLocked("✓", true, s.detail)
}

func (s *plainStep) Skip(reason string) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.writeLocked("–", false, cleanLine(reason))
}

func (s *plainStep) Fail(err error, hint string) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.writeLocked("✕", true, s.detail, msg, hint)
}
