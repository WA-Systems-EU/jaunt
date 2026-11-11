package ui

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"time"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func newTestLive(buf *lockedBuf) (Steps, chan time.Time, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	tick := make(chan time.Time)
	return newLive(buf, clk.now, tick, nil, nil), tick, clk
}

// lockedBuf lets the ticker goroutine and the test read and write safely.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// sendTick delivers a tick; the unbuffered send returns once the goroutine
// has received it, so a following Close/String sees the redraw (the redraw
// holds the model lock, which Close also takes).
func sendTick(tick chan time.Time) { tick <- time.Now() }

func TestLiveDrawsPendingAndSpinner(t *testing.T) {
	buf := &lockedBuf{}
	s, tick, _ := newTestLive(buf)
	s.Header("jauntbox", "ctx")
	s.Plan("one", "two", "three")
	s.Start("one")
	sendTick(tick)
	s.Close()
	out := stripANSI(buf.String())
	if !strings.Contains(out, "⠋") && !strings.Contains(out, "⠙") {
		t.Errorf("no spinner in %q", out)
	}
	for _, l := range []string{"one", "two", "three"} {
		if !strings.Contains(out, l) {
			t.Errorf("missing label %q", l)
		}
	}
	if !strings.Contains(out, "· two") {
		t.Errorf("no pending marker in %q", out)
	}
}

func TestLiveFinishedLinesStay(t *testing.T) {
	buf := &lockedBuf{}
	s, tick, clk := newTestLive(buf)
	s.Header("jauntbox", "ctx")
	s.Plan("one", "two", "three")
	st := s.Start("one")
	sendTick(tick)
	clk.adv(2 * time.Second)
	st.Done()
	s.Start("two")
	sendTick(tick)
	s.Close()
	out := buf.String()
	if !strings.Contains(stripANSI(out), "✓ one 2.0s") {
		t.Errorf("finished line missing: %q", stripANSI(out))
	}
	// the last erase happens after the finished line was printed: the line
	// "✓ one" must never be inside an erased region, i.e. appears once.
	if n := strings.Count(stripANSI(out), "✓ one"); n != 1 {
		t.Errorf("finished line printed %d times", n)
	}
	// cursor-up never exceeds the unfinished block height (3 plan lines max)
	for _, m := range regexp.MustCompile(`\x1b\[(\d+)A`).FindAllStringSubmatch(out, -1) {
		if n, _ := strconv.Atoi(m[1]); n > 3 {
			t.Errorf("cursor up %d exceeds block height", n)
		}
	}
	// a frame drawn right after Done begins with the finished line
	if i := strings.Index(stripANSI(out), "✓ one"); i < 0 {
		t.Error("no finished line")
	}
	frames := strings.Split(out, "\x1b[J")
	found := false
	for _, fr := range frames {
		if strings.HasPrefix(stripANSI(fr), "✓ one") {
			found = true
		}
	}
	if !found {
		t.Errorf("no frame starts with finished line: %q", frames)
	}
}

func TestLiveFailFinalises(t *testing.T) {
	buf := &lockedBuf{}
	s, tick, _ := newTestLive(buf)
	s.Plan("one", "two")
	st := s.Start("one")
	sendTick(tick)
	st.Fail(errors.New("boom"), "try again")
	s.Close()
	before := buf.String()
	// later ticks must not write (goroutine stopped); nonblocking send
	select {
	case tick <- time.Now():
		t.Error("ticker goroutine still running after Close")
	case <-time.After(50 * time.Millisecond):
	}
	if buf.String() != before {
		t.Error("writes after Close")
	}
	txt := stripANSI(before)
	if !strings.Contains(txt, "✕ one") || !strings.Contains(txt, "boom") || !strings.Contains(txt, "  try again") {
		t.Errorf("fail output: %q", txt)
	}
	if !strings.HasSuffix(before, "\n") {
		t.Errorf("does not end with newline: %q", before)
	}
	if strings.Contains(before[strings.LastIndex(before, "\n")-1:], "\x1b[") {
		t.Errorf("escape after last line: %q", before)
	}
}

func TestLiveCloseIdempotent(t *testing.T) {
	buf := &lockedBuf{}
	s, _, _ := newTestLive(buf)
	st := s.Start("one")
	s.Close()
	before := buf.String()
	s.Close()
	st.Done()
	if buf.String() != before {
		t.Errorf("second Close or late Done wrote: %q", buf.String())
	}
	if !strings.Contains(stripANSI(before), "– one 0.0s interrupted") {
		t.Errorf("interrupted line: %q", stripANSI(before))
	}
}

func TestLiveNoAltScreen(t *testing.T) {
	buf := &lockedBuf{}
	s, tick, _ := newTestLive(buf)
	s.Header("a", "b")
	s.Start("x").Done()
	sendTick(tick)
	s.End(true, "ok", "line")
	s.Close()
	if strings.Contains(buf.String(), "\x1b[?1049h") {
		t.Error("alt screen used")
	}
}

func TestLiveSemanticsMatchPlain(t *testing.T) {
	buf := &lockedBuf{}
	s, _, clk := newTestLive(buf)
	a := s.Start("warned")
	a.Warn("careful")
	clk.adv(time.Second)
	a.Done()
	a.Fail(errors.New("ignored"), "")
	s.Start("skipped").Skip("no secrets")
	s.End(false, "ignored summary", "only this")
	s.Close()
	out := stripANSI(buf.String())
	for _, w := range []string{"! warned 1.0s\n  careful\n", "– skipped no secrets\n", "  only this\n"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in %q", w, out)
		}
	}
	if strings.Contains(out, "ignored") || strings.Contains(out, "●") {
		t.Errorf("unexpected: %q", out)
	}
}

func TestLiveStartUnplannedAppends(t *testing.T) {
	buf := &lockedBuf{}
	s, _, _ := newTestLive(buf)
	s.Plan("one")
	s.Start("extra").Done()
	s.Close()
	out := stripANSI(buf.String())
	if !strings.Contains(out, "✓ extra") || !strings.Contains(out, "· one") {
		t.Errorf("got %q", out)
	}
}

func TestLiveTruncatesBlockToWidth(t *testing.T) {
	buf := &lockedBuf{}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	tick := make(chan time.Time)
	s := newLive(buf, clk.now, tick, func() int { return 20 }, nil)
	s.Plan("a very long step label indeed", "second")
	st := s.Start("a very long step label indeed")
	st.Detail("a long detail that cannot fit on one row")
	sendTick(tick)
	sendTick(tick)
	s.Close()
	out := buf.String()
	// Check every erased frame (all but the final one printed for good).
	frames := strings.Split(out, "\x1b[J")
	for _, fr := range frames[:len(frames)-1] {
		for _, ln := range strings.Split(strings.TrimSuffix(fr, "\n"), "\n") {
			if w := ansi.StringWidth(stripANSI(ln)); w > 19 {
				t.Errorf("line %q is %d columns", ln, w)
			}
		}
	}
	// cursor-up equals the number of lines in the frame it erases
	re := regexp.MustCompile(`(?:\x1b\[(\d+)A)?\x1b\[J`)
	idx := re.FindAllStringSubmatchIndex(out, -1)
	for k := 1; k < len(idx); k++ {
		frame := out[idx[k-1][1]:idx[k][0]]
		n := strings.Count(frame, "\n")
		if idx[k][2] < 0 {
			t.Errorf("frame %d erased without cursor-up", k)
			continue
		}
		if got, _ := strconv.Atoi(out[idx[k][2]:idx[k][3]]); got != n {
			t.Errorf("cursor-up %d, frame had %d lines", got, n)
		}
	}
}

func TestLiveFailLongErrorTruncatedInBlock(t *testing.T) {
	buf := &lockedBuf{}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	tick := make(chan time.Time)
	s := newLive(buf, clk.now, tick, func() int { return 20 }, nil)
	s.Plan("zero", "one")
	s.Start("one").Fail(errors.New("a very long error message that overflows"), "and a very long hint too")
	sendTick(tick)
	s.Close()
	frames := strings.Split(buf.String(), "\x1b[J")
	for _, fr := range frames[:len(frames)-1] {
		for _, ln := range strings.Split(strings.TrimSuffix(stripANSI(fr), "\n"), "\n") {
			if ansi.StringWidth(ln) > 19 {
				t.Errorf("%q too wide", ln)
			}
		}
	}
}

func TestLiveNoteKeepsTimeOrder(t *testing.T) {
	buf := &lockedBuf{}
	s, _, _ := newTestLive(buf)
	s.Plan("A", "B", "C")
	s.Start("A").Done()
	s.Note("n")
	s.Close()
	if got := screen(buf.String()); got != "✓ A 0.0s\nn\n· B\n· C\n" {
		t.Errorf("screen = %q", got)
	}
}

// screen replays cursor-up and erase-to-end over the output and returns what
// would be left on screen, ANSI stripped.
func screen(out string) string {
	re := regexp.MustCompile(`\x1b\[(\d*)A|\x1b\[J|\x1b\[[0-9;]*m`)
	var lines []string // completed lines; cursor is at the start of a new line
	pos := 0
	for _, m := range re.FindAllStringSubmatchIndex(out, -1) {
		lines = appendText(lines, out[pos:m[0]])
		pos = m[1]
		seq := out[m[0]:m[1]]
		switch {
		case strings.HasSuffix(seq, "A"):
			n := 1
			if m[2] != m[3] {
				n, _ = strconv.Atoi(out[m[2]:m[3]])
			}
			lines = lines[:len(lines)-n]
		case seq == "\x1b[J":
		}
	}
	lines = appendText(lines, out[pos:])
	return strings.Join(lines, "\n") + "\n"
}

func appendText(lines []string, t string) []string {
	if t == "" {
		return lines
	}
	return append(lines, strings.Split(strings.TrimSuffix(t, "\n"), "\n")...)
}

func TestLiveMultilineErrorSplitsLines(t *testing.T) {
	buf := &lockedBuf{}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	tick := make(chan time.Time)
	s := newLive(buf, clk.now, tick, func() int { return 40 }, nil)
	s.Plan("A", "B")
	st := s.Start("B")
	st.Detail("x\ty\nz")
	sendTick(tick)
	sendTick(tick) // the second send returns only after the first redraw
	if !strings.Contains(stripANSI(buf.String()), " B 0.0s x y z\n") {
		t.Errorf("detail not one line: %q", stripANSI(buf.String()))
	}
	st.Fail(errors.New("l1\nl2\r\nl3\twide"), "")
	sendTick(tick)
	s.Close()
	out := buf.String()
	if got := screen(out); !strings.Contains(got, "✕ B 0.0s x y z\n  l1\n  l2\n  l3 wide\n") {
		t.Errorf("screen = %q", got)
	}
	re := regexp.MustCompile(`(?:\x1b\[(\d+)A)?\x1b\[J`)
	idx := re.FindAllStringSubmatchIndex(out, -1)
	for k := 1; k < len(idx); k++ {
		frame := out[idx[k-1][1]:idx[k][0]]
		if idx[k][2] < 0 {
			continue
		}
		if got, _ := strconv.Atoi(out[idx[k][2]:idx[k][3]]); got != strings.Count(frame, "\n") {
			t.Errorf("cursor-up %d, frame had %d lines", got, strings.Count(frame, "\n"))
		}
		if strings.ContainsAny(frame, "\r\t") {
			t.Errorf("control char in block frame %q", frame)
		}
	}
}

func TestLiveMultilineNoteCountsRows(t *testing.T) {
	buf := &lockedBuf{}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	tick := make(chan time.Time)
	s := newLive(buf, clk.now, tick, func() int { return 40 }, nil)
	s.Plan("A")
	s.Note("n1\nn2")
	sendTick(tick)
	sendTick(tick)
	s.Close()
	if got := screen(buf.String()); got != "n1\nn2\n· A\n" {
		t.Errorf("screen = %q", got)
	}
	// a wrong cursor-up count would leave stale copies of "· A" on screen
}

const hostile = "a\x1b[2Kb\x1b[1Ac\v d\n"

func TestLiveHostileInputSanitised(t *testing.T) {
	buf := &lockedBuf{}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	tick := make(chan time.Time)
	s := newLive(buf, clk.now, tick, func() int { return 40 }, nil)
	s.Header("t\x1b[2J"+"\n", "c\r")
	s.Plan("P\x1b[31m", "B")
	st := s.Start("P\x1b[31m")
	st.Detail("d\x1b[2K\n\v")
	sendTick(tick)
	sendTick(tick)
	st.Fail(errors.New(hostile), hostile)
	s.Start("B").Skip("why\nnot\x1b[1A")
	s.End(true, "sum\x1b[2K\n", hostile)
	s.Close()
	out := buf.String()
	scr := screen(out)
	if strings.ContainsAny(scr, "\x1b\v") {
		t.Errorf("control chars on screen: %q", scr)
	}
	if !strings.Contains(scr, "  a") || !strings.Contains(scr, "– B why not\n") {
		t.Errorf("screen = %q", scr)
	}
	if strings.Contains(scr, "\n\n") {
		t.Errorf("blank line: %q", scr)
	}
	// only our own cursor-up/erase sequences may appear
	rest := regexp.MustCompile(`\x1b\[\d*A|\x1b\[J|\x1b\[[0-9;]*m`).ReplaceAllString(out, "")
	if strings.ContainsRune(rest, 0x1b) {
		t.Errorf("foreign escape in %q", rest)
	}
}
