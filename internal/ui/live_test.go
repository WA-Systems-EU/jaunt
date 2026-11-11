package ui

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func newTestLive(buf *lockedBuf) (Steps, chan time.Time, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	tick := make(chan time.Time)
	return NewLive(buf, clk.now, tick), tick, clk
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
