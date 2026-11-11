package ui

import (
	"strings"
	"sync"
)

// Recorder is a Steps that records what happened, for tests. The zero value
// is ready to use and it is safe for concurrent use.
type Recorder struct {
	Events []string

	mu     sync.Mutex
	open   []*recStep
	closed bool
}

func (r *Recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = append(r.Events, e)
}

func (r *Recorder) Header(title, context string) { r.add("header " + title + " " + context) }
func (r *Recorder) Plan(labels ...string)        { r.add("plan " + strings.Join(labels, ",")) }
func (r *Recorder) Note(line string)             { r.add("note " + line) }

// Close records "close" and "interrupted <label>" for each unfinished step.
func (r *Recorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.Events = append(r.Events, "close")
	for _, s := range r.open {
		s.done = true
		r.Events = append(r.Events, "interrupted "+s.label)
	}
	r.open = nil
}

func (r *Recorder) Start(label string) Step {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = append(r.Events, "start "+label)
	s := &recStep{r: r, label: label}
	r.open = append(r.open, s)
	return s
}

// End records "end ok <summary>" or "end fail", followed by " | <line>" for
// each line.
func (r *Recorder) End(ok bool, summary string, lines ...string) {
	e := "end fail"
	if ok {
		e = "end ok " + summary
	}
	for _, l := range lines {
		e += " | " + l
	}
	r.add(e)
}

type recStep struct {
	r     *Recorder
	label string
	done  bool
}

func (s *recStep) Detail(d string) { s.r.add("detail " + s.label + ": " + d) }
func (s *recStep) Warn(msg string) { s.r.add("warn " + s.label + ": " + msg) }

// finish records an outcome unless one was already reported.
func (s *recStep) finish(e string) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if s.done {
		return
	}
	s.done = true
	for i, o := range s.r.open {
		if o == s {
			s.r.open = append(s.r.open[:i], s.r.open[i+1:]...)
			break
		}
	}
	s.r.Events = append(s.r.Events, e)
}

func (s *recStep) Done()              { s.finish("done " + s.label) }
func (s *recStep) Skip(reason string) { s.finish("skip " + s.label + ": " + reason) }

func (s *recStep) Fail(err error, hint string) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.finish("fail " + s.label + ": " + msg)
}
