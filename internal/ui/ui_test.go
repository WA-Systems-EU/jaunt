package ui

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{200 * time.Millisecond, "0.2s"},
		{8100 * time.Millisecond, "8.1s"},
		{41 * time.Second, "41s"},
		{72 * time.Second, "1m12s"},
		{600 * time.Second, "10m0s"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) adv(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func runPlain(buf *bytes.Buffer) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewPlain(buf, clk.now)
	s.Header("jauntbox", "tsultrim · small (cpx42) · fsn1")
	s.Plan("Server created", "Unlock secrets", "Provisioning")
	st := s.Start("Server created")
	clk.adv(8100 * time.Millisecond)
	st.Detail("#42")
	st.Done()
	st = s.Start("Unlock secrets")
	clk.adv(900 * time.Millisecond)
	st.Warn("could not unlock: busy")
	st.Done()
	st = s.Start("Unlock secrets")
	st.Skip("no secrets set")
	st = s.Start("Provisioning")
	clk.adv(600 * time.Second)
	st.Fail(errors.New("boom"), "check the log")
	s.End(true, "Ready in 2m31s.", "ssh jauntbox-u")
	s.Close()
	s.Close()
}

func TestPlainLines(t *testing.T) {
	var buf bytes.Buffer
	runPlain(&buf)
	want := "jauntbox · tsultrim · small (cpx42) · fsn1\n✓ Server created 8.1s #42\n! Unlock secrets 0.9s\n  could not unlock: busy\n– Unlock secrets no secrets set\n✕ Provisioning 10m0s\n  boom\n  check the log\n● Ready in 2m31s.\n  ssh jauntbox-u\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestPlainEndNotOK(t *testing.T) {
	var buf bytes.Buffer
	s := NewPlain(&buf, time.Now)
	s.End(false, "ignored", "line one", "line two")
	if want := "  line one\n  line two\n"; buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}

func TestPlainNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	runPlain(&buf)
	if bytes.IndexByte(buf.Bytes(), 0x1b) >= 0 {
		t.Error("plain output contains an escape byte")
	}
}

func TestRecorderEvents(t *testing.T) {
	r := &Recorder{}
	r.Header("jauntbox", "ctx")
	r.Plan("A", "B")
	a := r.Start("A")
	a.Detail("d")
	a.Done()
	b := r.Start("B")
	b.Warn("careful")
	b.Done()
	c := r.Start("C")
	c.Skip("nope")
	d := r.Start("D")
	d.Fail(errors.New("bad"), "hint")
	r.Note("n")
	r.End(true, "Ready in 1s.")
	r.Close()
	want := []string{
		"header jauntbox ctx", "plan A,B",
		"start A", "detail A: d", "done A",
		"start B", "warn B: careful", "done B",
		"start C", "skip C: nope",
		"start D", "fail D: bad",
		"note n", "end ok Ready in 1s.", "close",
	}
	if !reflect.DeepEqual(r.Events, want) {
		t.Errorf("events:\n%q\nwant:\n%q", r.Events, want)
	}
}

func TestStepSingleOutcome(t *testing.T) {
	r := &Recorder{}
	s := r.Start("X")
	s.Done()
	s.Fail(errors.New("late"), "")
	if want := []string{"start X", "done X"}; !reflect.DeepEqual(r.Events, want) {
		t.Errorf("got %q want %q", r.Events, want)
	}
	var buf bytes.Buffer
	p := NewPlain(&buf, time.Now).Start("Y")
	p.Done()
	p.Fail(errors.New("late"), "")
	if strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("plain printed more than one outcome: %q", buf.String())
	}
}

func TestNop(t *testing.T) {
	s := Nop()
	s.Header("a", "b")
	s.Plan("x")
	st := s.Start("x")
	st.Detail("d")
	st.Warn("w")
	st.Done()
	s.Note("n")
	s.End(true, "ok")
	s.Close()
}

func TestLiveModeDecision(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tty := func(int) bool { return true }
	notty := func(int) bool { return false }
	f := os.Stderr
	cases := []struct {
		name  string
		plain bool
		env   map[string]string
		term  func(int) bool
		want  bool
	}{
		{"clear", false, nil, tty, true},
		{"plain flag", true, nil, tty, false},
		{"NO_COLOR set", false, map[string]string{"NO_COLOR": "1"}, tty, false},
		{"NO_COLOR empty", false, map[string]string{"NO_COLOR": ""}, tty, true},
		{"TERM dumb", false, map[string]string{"TERM": "dumb"}, tty, false},
		{"TERM xterm", false, map[string]string{"TERM": "xterm-256color"}, tty, true},
		{"not a terminal", false, nil, notty, false},
	}
	for _, c := range cases {
		if got := Live(f, c.plain, env(c.env), c.term); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if Live(&bytes.Buffer{}, false, env(nil), tty) {
		t.Error("non-file writer must not be live")
	}
}

func TestPlainConcurrentSingleOutcome(t *testing.T) {
	var buf bytes.Buffer
	s := NewPlain(&buf, time.Now)
	st := s.Start("X")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.Detail("d")
			st.Warn("w")
			st.Done()
			st.Fail(errors.New("e"), "h")
		}()
	}
	wg.Wait()
	s.Close()
	lines := 0
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, "✓ ") || strings.HasPrefix(l, "! ") || strings.HasPrefix(l, "✕ ") {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("want exactly one outcome line, got %d in %q", lines, buf.String())
	}
}

func TestRecorderConcurrent(t *testing.T) {
	r := &Recorder{}
	st := r.Start("X")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Note("n")
			st.Done()
			st.Fail(errors.New("e"), "")
		}()
	}
	wg.Wait()
	n := 0
	for _, e := range r.Events {
		if e == "done X" || strings.HasPrefix(e, "fail X") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want one outcome, got %d: %q", n, r.Events)
	}
}

func TestPlainCloseInterrupted(t *testing.T) {
	var buf bytes.Buffer
	clk := &fakeClock{t: time.Unix(0, 0)}
	s := NewPlain(&buf, clk.now)
	a := s.Start("A")
	b := s.Start("B")
	c := s.Start("C")
	clk.adv(3 * time.Second)
	b.Done()
	clk.adv(2 * time.Second)
	s.Close()
	s.Close()
	a.Done()
	c.Fail(errors.New("late"), "")
	want := "✓ B 3.0s\n– A 5.0s interrupted\n– C 5.0s interrupted\n"
	if buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}

func TestRecorderCloseAndEnd(t *testing.T) {
	r := &Recorder{}
	a := r.Start("A")
	r.Start("B").Done()
	r.End(true, "Ready.", "l1", "l2")
	r.End(false, "x", "l1", "l2")
	r.Close()
	a.Done()
	want := []string{"start A", "start B", "done B", "end ok Ready. | l1 | l2", "end fail | l1 | l2", "close", "interrupted A"}
	if !reflect.DeepEqual(r.Events, want) {
		t.Errorf("got %q want %q", r.Events, want)
	}
}

func TestPlainCloseThreeOpen(t *testing.T) {
	var buf bytes.Buffer
	clk := &fakeClock{t: time.Unix(0, 0)}
	s := NewPlain(&buf, clk.now)
	a, b, c := s.Start("A"), s.Start("B"), s.Start("C")
	clk.adv(2 * time.Second)
	s.Close()
	want := "– A 2.0s interrupted\n– B 2.0s interrupted\n– C 2.0s interrupted\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
	b.Done()
	a.Skip("x")
	c.Fail(errors.New("late"), "")
	s.Close()
	if buf.String() != want {
		t.Errorf("later calls printed: %q", buf.String())
	}
}

func TestPlainMultilineFailIndented(t *testing.T) {
	var buf bytes.Buffer
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewPlain(&buf, clk.now)
	s.Start("x").Fail(errors.New("one\ntwo"), "hint")
	if want := "✕ x 0.0s\n  one\n  two\n  hint\n"; buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}

func TestPlainHostileInputSanitised(t *testing.T) {
	var buf bytes.Buffer
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewPlain(&buf, clk.now)
	s.Header("t\x1b[2J", "c\r")
	st := s.Start("P\x1b[31m")
	st.Detail("d\n")
	st.Warn("w\x1b[2K\v")
	st.Fail(errors.New(hostile), hostile)
	s.Start("B").Skip("why\nnot\x1b[1A")
	s.Note("n\x1b[1A\n")
	s.End(true, "sum\x1b[2K\n", hostile)
	out := buf.String()
	if strings.ContainsAny(out, "\x1b\v\r") {
		t.Errorf("control chars: %q", out)
	}
	if strings.Contains(out, "\n\n") {
		t.Errorf("blank line: %q", out)
	}
	if !strings.Contains(out, "– B why not\n") || !strings.Contains(out, "✕ P d 0.0s") && !strings.Contains(out, "✕ P 0.0s d") {
		t.Errorf("got %q", out)
	}
}
