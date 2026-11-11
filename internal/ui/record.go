package ui

import "strings"

// Recorder is a Steps that records what happened, for tests.
type Recorder struct{ Events []string }

func (r *Recorder) add(e string) { r.Events = append(r.Events, e) }

func (r *Recorder) Header(title, context string) { r.add("header " + title + " " + context) }
func (r *Recorder) Plan(labels ...string)        { r.add("plan " + strings.Join(labels, ",")) }
func (r *Recorder) Note(line string)             { r.add("note " + line) }
func (r *Recorder) Close()                       {}

func (r *Recorder) Start(label string) Step {
	r.add("start " + label)
	return &recStep{r: r, label: label}
}

func (r *Recorder) End(ok bool, summary string, lines ...string) {
	k := "fail"
	if ok {
		k = "ok"
	}
	r.add("end " + k + " " + summary)
}

type recStep struct {
	r     *Recorder
	label string
	done  bool
	warn  bool
}

func (s *recStep) Detail(d string) { s.r.add("detail " + s.label + ": " + d) }
func (s *recStep) Warn(msg string) { s.warn = true; s.r.add("warn " + s.label + ": " + msg) }

func (s *recStep) Done() {
	if s.done {
		return
	}
	s.done = true
	s.r.add("done " + s.label)
}

func (s *recStep) Skip(reason string) {
	if s.done {
		return
	}
	s.done = true
	s.r.add("skip " + s.label + ": " + reason)
}

func (s *recStep) Fail(err error, hint string) {
	if s.done {
		return
	}
	s.done = true
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.r.add("fail " + s.label + ": " + msg)
}
