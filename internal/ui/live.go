package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type itemState int

const (
	statePending itemState = iota
	stateRunning
	stateFinished
)

// item is one entry of the output: a static text block or a step.
type item struct {
	state itemState
	lines []string // finished rendering (text items and finished steps)

	// step fields
	label  string
	start  time.Time
	detail string
	warns  []string
}

type live struct {
	mu     sync.Mutex
	w      io.Writer
	now    func() time.Time
	items  []*item
	drawn  int // lines of the unfinished block currently on screen
	frame  int
	closed bool

	done chan struct{}
	wg   sync.WaitGroup

	dim, teal, yellow, red, bold lipgloss.Style
}

// NewLive returns the animated renderer for a terminal: the unfinished steps
// are redrawn in place on every tick, finished lines are printed once and
// stay in the scrollback. tick nil means an internal 100 ms ticker.
func NewLive(w io.Writer, now func() time.Time, tick <-chan time.Time) Steps {
	if now == nil {
		now = time.Now
	}
	r := lipgloss.NewRenderer(w)
	r.SetColorProfile(termenv.TrueColor)
	l := &live{
		w: w, now: now, done: make(chan struct{}),
		dim:    r.NewStyle().Foreground(lipgloss.Color("#6f8196")),
		teal:   r.NewStyle().Foreground(lipgloss.Color("#4fc2b9")),
		yellow: r.NewStyle().Foreground(lipgloss.Color("#f3d36b")),
		red:    r.NewStyle().Foreground(lipgloss.Color("#ff6a5c")),
		bold:   r.NewStyle().Bold(true),
	}
	var stop func()
	if tick == nil {
		t := time.NewTicker(100 * time.Millisecond)
		tick = t.C
		stop = t.Stop
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		if stop != nil {
			defer stop()
		}
		for {
			select {
			case <-l.done:
				return
			case <-tick:
				l.mu.Lock()
				if !l.closed {
					l.frame++
					l.redrawLocked()
				}
				l.mu.Unlock()
			}
		}
	}()
	return l
}

func (l *live) blockLine(it *item) string {
	switch it.state {
	case statePending:
		return l.dim.Render("· " + it.label)
	default:
		s := l.yellow.Render(spinnerFrames[l.frame%len(spinnerFrames)]) + " " + it.label +
			" " + l.dim.Render(FormatDuration(l.now().Sub(it.start)))
		if it.detail != "" {
			s += " " + l.dim.Render(it.detail)
		}
		return s
	}
}

// redrawLocked erases the previous block, prints the finished lines at the
// head of the list for good and draws the rest. The caller holds l.mu.
func (l *live) redrawLocked() {
	var b strings.Builder
	if l.drawn > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", l.drawn)
	}
	b.WriteString("\x1b[J")
	i := 0
	for i < len(l.items) && l.items[i].state == stateFinished {
		for _, ln := range l.items[i].lines {
			b.WriteString(ln + "\n")
		}
		i++
	}
	l.items = l.items[i:]
	n := 0
	for _, it := range l.items {
		if it.state == stateFinished {
			for _, ln := range it.lines {
				b.WriteString(ln + "\n")
				n++
			}
			continue
		}
		b.WriteString(l.blockLine(it) + "\n")
		n++
	}
	l.drawn = n
	io.WriteString(l.w, b.String())
}

func (l *live) addText(lines ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.items = append(l.items, &item{state: stateFinished, lines: lines})
	l.redrawLocked()
}

func (l *live) Header(title, context string) {
	s := l.bold.Render(title)
	if context != "" {
		s += " " + l.dim.Render("· "+context)
	}
	l.addText(s)
}

func (l *live) Note(line string) { l.addText(l.dim.Render(line)) }

func (l *live) Plan(labels ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	for _, lb := range labels {
		l.items = append(l.items, &item{state: statePending, label: lb})
	}
	l.redrawLocked()
}

func (l *live) Start(label string) Step {
	l.mu.Lock()
	defer l.mu.Unlock()
	it := &item{state: stateRunning, label: label, start: l.now()}
	if l.closed {
		it.state = stateFinished // ignored: never drawn
		return &liveStep{l: l, it: it}
	}
	replaced := false
	for i, o := range l.items {
		if o.state == statePending && o.label == label {
			l.items[i] = it
			replaced = true
			break
		}
	}
	if !replaced {
		l.items = append(l.items, it)
	}
	l.redrawLocked()
	return &liveStep{l: l, it: it}
}

func (l *live) End(ok bool, summary string, lines ...string) {
	var out []string
	if ok {
		out = append(out, l.teal.Render("●")+" "+summary)
	}
	for _, ln := range lines {
		out = append(out, "  "+l.dim.Render(ln))
	}
	if len(out) > 0 {
		l.addText(out...)
	}
}

// Close finalises every running step as interrupted, draws the last frame,
// stops the ticker and leaves the cursor on a fresh line below everything.
func (l *live) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	for _, it := range l.items {
		if it.state == stateRunning {
			l.finishLocked(it, "–", true, "interrupted")
		}
	}
	// What is still pending stays on screen as dim lines: print it for good.
	l.redrawLocked()
	for _, it := range l.items {
		it.state = stateFinished
	}
	l.closed = true
	close(l.done)
	l.mu.Unlock()
	l.wg.Wait()
}

// finishLocked turns a running step into its final lines; the caller holds
// l.mu. msgs are indented below.
func (l *live) finishLocked(it *item, sym string, withDur bool, extra string, msgs ...string) {
	if it.state != stateRunning {
		return
	}
	it.state = stateFinished
	var head string
	switch sym {
	case "✓":
		head = l.teal.Render(sym)
	case "!":
		head = l.yellow.Render(sym)
	case "✕":
		head = l.red.Render(sym)
	default:
		head = l.dim.Render(sym)
	}
	head += " " + it.label
	if withDur {
		head += " " + l.dim.Render(FormatDuration(l.now().Sub(it.start)))
	}
	if extra != "" {
		head += " " + l.dim.Render(extra)
	}
	it.lines = []string{head}
	for _, m := range msgs {
		if m != "" {
			it.lines = append(it.lines, "  "+m)
		}
	}
}

type liveStep struct {
	l  *live
	it *item
}

func (s *liveStep) Detail(d string) {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	s.it.detail = d
}

func (s *liveStep) Warn(msg string) {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	s.it.warns = append(s.it.warns, msg)
}

func (s *liveStep) outcome(sym string, withDur bool, extra string, msgs ...string) {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	if s.l.closed || s.it.state != stateRunning {
		return
	}
	s.l.finishLocked(s.it, sym, withDur, extra, msgs...)
	s.l.redrawLocked()
}

func (s *liveStep) Done() {
	s.l.mu.Lock()
	warns, detail := s.it.warns, s.it.detail
	s.l.mu.Unlock()
	if len(warns) > 0 {
		s.outcome("!", true, detail, warns...)
		return
	}
	s.outcome("✓", true, detail)
}

func (s *liveStep) Skip(reason string) { s.outcome("–", false, reason) }

func (s *liveStep) Fail(err error, hint string) {
	s.l.mu.Lock()
	detail := s.it.detail
	s.l.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.outcome("✕", true, detail, msg, hint)
}
