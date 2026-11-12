package jenkins

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const defaultTestTimeout = 5 * time.Second

// fakeLog serves the progressiveText endpoint with the same header contract the
// real controller uses: X-Text-Size is the total log size, X-More-Data says
// whether the build is still writing.
type fakeLog struct {
	mu   sync.Mutex
	log  string
	more bool
}

func (f *fakeLog) set(log string, more bool) {
	f.mu.Lock()
	f.log, f.more = log, more
	f.mu.Unlock()
}

func (f *fakeLog) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/logText/progressiveText") {
			http.NotFound(w, r)
			return
		}
		start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		f.mu.Lock()
		logText, more := f.log, f.more
		f.mu.Unlock()
		if start > int64(len(logText)) {
			start = int64(len(logText))
		}
		body := logText[start:]
		w.Header().Set("Content-Type", "text/plain;charset=utf-8")
		w.Header().Set("X-Text-Size", strconv.Itoa(len(logText)))
		w.Header().Set("X-More-Data", strconv.FormatBool(more))
		w.Write([]byte(body))
	})
}

func newLogClient(t *testing.T, f *fakeLog) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c, err := NewClient(Options{BaseURL: srv.URL, Timeout: defaultTestTimeout})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// TestReadLogRoundTrip is the invariant that matters: reading chunk by chunk
// using next_start must reproduce the log exactly, with nothing lost or repeated.
func TestReadLogRoundTrip(t *testing.T) {
	// Multi-byte characters, because that is where a byte cursor breaks.
	full := strings.Repeat("第一行内容\n第二行内容\n", 40)
	f := &fakeLog{}
	f.set(full, false)
	c := newLogClient(t, f)

	var got strings.Builder
	start := int64(0)
	for i := 0; i < 1000; i++ {
		chunk, err := c.ReadLog(t.Context(), "job", RefLastBuild, start)
		if err != nil {
			t.Fatalf("ReadLog: %v", err)
		}
		got.WriteString(chunk.Text)
		if !chunk.MoreData {
			break
		}
		if chunk.NextStart == start {
			t.Fatalf("cursor did not advance: stuck at %d", start)
		}
		start = chunk.NextStart
	}
	if got.String() != full {
		t.Fatalf("round trip mismatch:\n got %q\nwant %q", got.String(), full)
	}
}

// TestReadLogAlignsToLineBoundary covers the bug the real controller exposed:
// a cursor dropped into the middle of a UTF-8 character must not produce a
// broken character or a half line.
func TestReadLogAlignsToLineBoundary(t *testing.T) {
	full := "第一行内容\n第二行内容\n第三行内容\n"
	f := &fakeLog{}
	f.set(full, false)
	c := newLogClient(t, f)

	// Byte 4 is inside "一": the worst case a naive cursor can hit.
	chunk, err := c.ReadLog(t.Context(), "job", RefLastBuild, 4)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if !utf8.ValidString(chunk.Text) {
		t.Fatalf("returned invalid UTF-8: %q", chunk.Text)
	}
	if strings.HasPrefix(chunk.Text, "行内容") {
		t.Fatalf("returned the tail of a line: %q", chunk.Text)
	}
	if want := "第二行内容\n第三行内容\n"; chunk.Text != want {
		t.Fatalf("got %q, want %q", chunk.Text, want)
	}
}

// TestReadLogHoldsBackPartialLine proves a line is never emitted in halves: while
// the build is running, an unterminated trailing line is withheld, and its bytes
// are returned once the newline arrives.
func TestReadLogHoldsBackPartialLine(t *testing.T) {
	f := &fakeLog{}
	f.set("line1\nline2-partial", true)
	c := newLogClient(t, f)

	chunk, err := c.ReadLog(t.Context(), "job", RefLastBuild, 0)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if chunk.Text != "line1\n" {
		t.Fatalf("got %q, want %q", chunk.Text, "line1\n")
	}
	if chunk.NextStart != int64(len("line1\n")) {
		t.Fatalf("next_start = %d, want %d", chunk.NextStart, len("line1\n"))
	}

	// The line completes.
	f.set("line1\nline2-partial\n", false)
	chunk2, err := c.ReadLog(t.Context(), "job", RefLastBuild, chunk.NextStart)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if chunk2.Text != "line2-partial\n" {
		t.Fatalf("got %q, want %q", chunk2.Text, "line2-partial\n")
	}
}

// TestReadLogEmitsFinalUnterminatedLine: when the build has ended, a log that does
// not end in a newline must still be delivered, or the last line vanishes.
func TestReadLogEmitsFinalUnterminatedLine(t *testing.T) {
	f := &fakeLog{}
	f.set("line1\nline2", false)
	c := newLogClient(t, f)

	chunk, err := c.ReadLog(t.Context(), "job", RefLastBuild, 0)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if chunk.Text != "line1\nline2" {
		t.Fatalf("got %q", chunk.Text)
	}
	if chunk.NextStart != int64(len("line1\nline2")) {
		t.Fatalf("next_start = %d, total = %d", chunk.NextStart, len("line1\nline2"))
	}
}

// TestReadLogFollowsGrowingLog simulates following a build to completion, which is
// the actual agent workflow.
func TestReadLogFollowsGrowingLog(t *testing.T) {
	f := &fakeLog{}
	c := newLogClient(t, f)
	f.set("step 1\n", true)

	chunk, err := c.ReadLog(t.Context(), "job", RefLastBuild, 0)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if chunk.Text != "step 1\n" || !chunk.MoreData {
		t.Fatalf("got %+v", chunk)
	}

	f.set("step 1\nstep 2\n", true)
	chunk, err = c.ReadLog(t.Context(), "job", RefLastBuild, chunk.NextStart)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if chunk.Text != "step 2\n" {
		t.Fatalf("got %q, want %q", chunk.Text, "step 2\n")
	}

	f.set("step 1\nstep 2\nDONE\n", false)
	chunk, err = c.ReadLog(t.Context(), "job", RefLastBuild, chunk.NextStart)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if chunk.Text != "DONE\n" || chunk.MoreData {
		t.Fatalf("got %+v", chunk)
	}
}

func TestTailLogKeepsWholeLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "line %03d\n", i)
	}
	full := b.String()
	f := &fakeLog{}
	f.set(full, false)
	c := newLogClient(t, f)

	text, total, more, err := c.TailLog(t.Context(), "job", RefLastBuild, 10)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if total != int64(len(full)) {
		t.Errorf("total = %d, want %d", total, len(full))
	}
	if more {
		t.Error("more_data should be false for a finished build")
	}
	if n := CountLines(text); n != 10 {
		t.Fatalf("got %d lines, want 10: %q", n, text)
	}
	if !strings.HasPrefix(text, "line 491") {
		t.Fatalf("tail is not the end of the log: %q", text)
	}
	if !utf8.ValidString(text) {
		t.Fatal("tail produced invalid UTF-8")
	}
}

func TestCountLines(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"a":      1,
		"a\n":    1,
		"a\nb":   2,
		"a\nb\n": 2,
	}
	for in, want := range cases {
		if got := CountLines(in); got != want {
			t.Errorf("CountLines(%q) = %d, want %d", in, got, want)
		}
	}
}
