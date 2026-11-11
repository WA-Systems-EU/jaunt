package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type plain struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
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
	if context == "" {
		p.printf("%s\n", title)
		return
	}
	p.printf("%s · %s\n", title, context)
}

func (p *plain) Plan(...string) {}

func (p *plain) Note(line string) { p.printf("%s\n", line) }

func (p *plain) Start(label string) Step {
	return &plainStep{p: p, label: label, start: p.now()}
}

func (p *plain) End(ok bool, summary string, lines ...string) {
	var b strings.Builder
	if ok {
		fmt.Fprintf(&b, "● %s\n", summary)
	}
	for _, l := range lines {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	p.printf("%s", b.String())
}

func (p *plain) Close() {}

type plainStep struct {
	p      *plain
	label  string
	start  time.Time
	detail string
	warns  []string
	done   bool
}

func (s *plainStep) Detail(d string) { s.detail = d }
func (s *plainStep) Warn(msg string) { s.warns = append(s.warns, msg) }

func (s *plainStep) finish(sym string, withDuration bool, extra string, msgs ...string) {
	if s.done {
		return
	}
	s.done = true
	var b strings.Builder
	b.WriteString(sym + " " + s.label)
	if withDuration {
		b.WriteString(" " + FormatDuration(s.p.now().Sub(s.start)))
	}
	if extra != "" {
		b.WriteString(" " + extra)
	}
	b.WriteString("\n")
	for _, m := range msgs {
		if m != "" {
			fmt.Fprintf(&b, "  %s\n", m)
		}
	}
	s.p.printf("%s", b.String())
}

func (s *plainStep) Done() {
	if len(s.warns) > 0 {
		s.finish("!", true, s.detail, s.warns...)
		return
	}
	s.finish("✓", true, s.detail)
}

func (s *plainStep) Skip(reason string) { s.finish("–", false, reason) }

func (s *plainStep) Fail(err error, hint string) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.finish("✕", true, s.detail, msg, hint)
}
