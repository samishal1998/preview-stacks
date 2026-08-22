// Package log is the logging seam: the CLI wants progress on stdout, the API wants the same events
// captured per job and streamed to a browser, so the orchestrator writes to a Sink and each
// consumer passes its own.
package log

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Level is an event's severity.
type Level string

const (
	Info  Level = "info"
	Step  Level = "step"
	Warn  Level = "warn"
	Error Level = "error"
)

// Event is one log line. JSON shape matches the SSE frame the UI reads.
type Event struct {
	// Seq is monotonic within a sink; useful for resuming an SSE stream.
	Seq     int64  `json:"seq"`
	At      int64  `json:"at"`
	Level   Level  `json:"level"`
	Message string `json:"message"`
}

// Sink receives events.
type Sink interface {
	Emit(level Level, message string)
}

// Console writes to stdout (errors to stderr). What the CLI uses.
type Console struct{}

// Emit prints.
func (Console) Emit(level Level, message string) {
	if level == Error {
		fmt.Fprintln(os.Stderr, message)
		return
	}
	fmt.Fprintln(os.Stdout, message)
}

// Writer is Console over explicit streams — what the CLI uses so a test can capture its output.
type Writer struct {
	Out io.Writer
	Err io.Writer
}

// Emit prints to Err for errors, Out otherwise.
func (w Writer) Emit(level Level, message string) {
	if level == Error {
		fmt.Fprintln(w.Err, message)
		return
	}
	fmt.Fprintln(w.Out, message)
}

// Null discards everything. For tests that assert on results rather than output.
type Null struct{}

// Emit does nothing.
func (Null) Emit(Level, string) {}

// Buffer keeps every event and notifies a callback. What the API uses: the job holds the buffer so
// a browser attaching late still gets the whole history, then live updates.
//
// Owner: the job that created it. The mutex guards Events against the SSE reader that snapshots it
// while the job goroutine appends; OnEvent is called OUTSIDE the mutex (rule 14).
type Buffer struct {
	mu      sync.Mutex
	events  []Event
	seq     int64
	OnEvent func(Event)
}

// NewBuffer returns an empty buffer.
func NewBuffer(onEvent func(Event)) *Buffer {
	return &Buffer{events: []Event{}, OnEvent: onEvent}
}

// Emit appends and notifies.
func (b *Buffer) Emit(level Level, message string) {
	b.mu.Lock()
	b.seq++
	e := Event{Seq: b.seq, At: time.Now().UnixMilli(), Level: level, Message: message}
	b.events = append(b.events, e)
	fn := b.OnEvent
	b.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

// Events is a copy of everything emitted so far — never nil.
func (b *Buffer) Events() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.events))
	copy(out, b.events)
	return out
}
