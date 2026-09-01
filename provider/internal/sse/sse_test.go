package sse

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReaderSplitsEvents(t *testing.T) {
	stream := "" +
		": a keep-alive comment\n" +
		"event: message_start\n" +
		"data: {\"a\":1}\n" +
		"\n" +
		"data: first line\n" +
		"data: second line\n" +
		"\r\n" +
		"data: [DONE]\n\n"

	r := NewReader(strings.NewReader(stream))

	first, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if first.Name != "message_start" || string(first.Data) != `{"a":1}` {
		t.Errorf("first event wrong: %q %q", first.Name, first.Data)
	}

	second, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	// Two data lines in one event join with a newline, per the spec.
	if string(second.Data) != "first line\nsecond line" {
		t.Errorf("multi-line data wrong: %q", second.Data)
	}
	if second.Name != "" {
		t.Errorf("the event name should not carry over: %q", second.Name)
	}

	third, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(third.Data) != "[DONE]" {
		t.Errorf("third event wrong: %q", third.Data)
	}

	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("want EOF at the end, got %v", err)
	}
}

func TestReaderReturnsAnUnterminatedFinalEvent(t *testing.T) {
	// A host that closes the socket without a blank line still said something.
	r := NewReader(strings.NewReader("data: last words"))
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(ev.Data) != "last words" {
		t.Errorf("got %q", ev.Data)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("want EOF, got %v", err)
	}
}

func TestReaderSurvivesAHugePayload(t *testing.T) {
	// A chunk carrying a screenshot is far past any line-scanner default.
	big := strings.Repeat("x", 1<<20)
	r := NewReader(strings.NewReader("data: " + big + "\n\n"))
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(ev.Data) != len(big) {
		t.Errorf("payload truncated at %d bytes", len(ev.Data))
	}
}

func TestWriterSetsTheStreamingHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	if err := w.Send([]byte(`{"x":1}`)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := w.Done(); err != nil {
		t.Fatalf("Done: %v", err)
	}

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
	want := "data: {\"x\":1}\n\ndata: [DONE]\n\n"
	if rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	payloads := []string{`{"one":1}`, "two\nlines", `{"three":3}`}
	for _, p := range payloads {
		if err := w.Send([]byte(p)); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	r := NewReader(strings.NewReader(rec.Body.String()))
	for _, want := range payloads {
		ev, err := r.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if string(ev.Data) != want {
			t.Errorf("got %q, want %q", ev.Data, want)
		}
	}
}
