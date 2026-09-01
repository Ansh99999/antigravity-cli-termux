// Package sse reads and writes text/event-streams. It exists because all three
// dialects stream over SSE and a `data:` payload can be split across lines, hold
// a megabyte of base64, or be preceded by keep-alive comments — none of which a
// line scanner with a fixed token limit survives.
package sse

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Event is one dispatched event.
type Event struct {
	Name string
	Data []byte
}

// Reader yields events from a stream.
type Reader struct {
	br   *bufio.Reader
	name string
	data bytes.Buffer
}

// NewReader wraps a response body.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 64*1024)}
}

// Next returns the next event, or io.EOF when the stream ends. A final event
// left unterminated by a blank line is still returned.
func (r *Reader) Next() (Event, error) {
	for {
		line, err := r.br.ReadString('\n')
		if err != nil {
			if len(line) > 0 {
				if ev, ok := r.consume(line); ok {
					return ev, nil
				}
			}
			if ev, ok := r.dispatch(); ok {
				return ev, nil
			}
			if err == io.EOF {
				return Event{}, io.EOF
			}
			return Event{}, err
		}
		if ev, ok := r.consume(line); ok {
			return ev, nil
		}
	}
}

// consume folds one line in, reporting an event when the line ended one.
func (r *Reader) consume(line string) (Event, bool) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return r.dispatch()
	}
	if strings.HasPrefix(line, ":") {
		return Event{}, false // a comment, which is how hosts keep the socket warm
	}
	field, value, found := strings.Cut(line, ":")
	if !found {
		field, value = line, ""
	}
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		r.name = value
	case "data":
		if r.data.Len() > 0 {
			r.data.WriteByte('\n')
		}
		r.data.WriteString(value)
	}
	return Event{}, false
}

func (r *Reader) dispatch() (Event, bool) {
	if r.data.Len() == 0 && r.name == "" {
		return Event{}, false
	}
	ev := Event{Name: r.name, Data: append([]byte(nil), r.data.Bytes()...)}
	r.name = ""
	r.data.Reset()
	return ev, true
}

// Writer emits an event stream to a client, flushing each event so a reply
// appears as it is generated rather than when the socket closes.
type Writer struct {
	w     io.Writer
	flush http.Flusher
}

// NewWriter prepares an SSE response. It sets the headers, so call it before
// anything is written.
func NewWriter(w http.ResponseWriter) *Writer {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := &Writer{w: w}
	if f, ok := w.(http.Flusher); ok {
		out.flush = f
		f.Flush()
	}
	return out
}

// Send writes one event. A payload holding newlines is split across `data:`
// lines the way the format requires — the reader joins them back. JSON never
// needs this, but a passthrough relays whatever the host sent.
func (w *Writer) Send(payload []byte) error {
	for line := range bytes.SplitSeq(payload, []byte("\n")) {
		if _, err := fmt.Fprintf(w.w, "data: %s\n", line); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w.w, "\n"); err != nil {
		return err
	}
	if w.flush != nil {
		w.flush.Flush()
	}
	return nil
}

// Done writes the terminator some clients expect. Gemini's own streams do not
// send one, so this is only used where a dialect calls for it.
func (w *Writer) Done() error { return w.Send([]byte("[DONE]")) }
