// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package cliprint renders demo CLI events in two interchangeable
// shapes: a colored, emoji-tagged "pretty" form for operators watching
// a live demo, and a JSON Lines form for piping into a log collector
// or test harness. Both demo binaries (publisher, subscriber) emit the
// same Event type so the on-screen story stays consistent.
package cliprint

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Kind identifies the kind of event being rendered. The mapping to a
// canonical lowercase string is the JSON wire form.
type Kind int

const (
	KindSend Kind = iota
	KindAck
	KindAckError
	KindNotification
	KindInfo
	KindError
)

// String returns the canonical lowercase name used in JSON output.
func (k Kind) String() string {
	switch k {
	case KindSend:
		return "send"
	case KindAck:
		return "ack"
	case KindAckError:
		return "ack_error"
	case KindNotification:
		return "notification"
	case KindInfo:
		return "info"
	case KindError:
		return "error"
	}
	return "unknown"
}

// Status is the high-level outcome of an event. It drives the emoji
// indicator in pretty mode and the "status" field in JSON mode.
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusFail
	StatusInfo
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	case StatusFail:
		return "fail"
	case StatusInfo:
		return "info"
	}
	return "unknown"
}

// Field is one ordered key/value pair attached to an event. Slice
// order is preserved in pretty rendering; JSON renders as an object.
type Field struct {
	K, V string
}

// Event is the rendered unit. Time is captured by the caller so tests
// can pin output deterministically.
type Event struct {
	Time   time.Time
	Kind   Kind
	Status Status
	Label  string  // e.g. "ORU^R01", "ACK", "lab-results"
	Fields []Field // ordered key/value pairs
	Msg    string  // free-form trailing message ("sent", "OK", error text)
}

// Options configures a Formatter at construction.
//
//	Pretty=true  → colored ANSI output with emoji indicators.
//	Pretty=false → newline-delimited JSON, one event per line.
//
// NoColor=true forces ANSI off even when Pretty=true. The NO_COLOR
// environment variable (https://no-color.org) does the same and wins
// over NoColor=false.
type Options struct {
	Pretty  bool
	NoColor bool
}

// Formatter writes Events to a sink. It is safe for concurrent use:
// each Emit performs exactly one Write so two goroutines cannot tear
// an ANSI sequence in half or interleave a JSON object.
type Formatter struct {
	w       io.Writer
	pretty  bool
	noColor bool
	mu      sync.Mutex
}

// NewFormatter builds a Formatter wired to w. The decision to colorize
// is made once here: NoColor or NO_COLOR (any non-empty value) suppresses
// ANSI for the lifetime of the formatter.
func NewFormatter(w io.Writer, o Options) *Formatter {
	noColor := o.NoColor
	if !noColor && os.Getenv("NO_COLOR") != "" {
		noColor = true
	}
	return &Formatter{w: w, pretty: o.Pretty, noColor: noColor}
}

// Emit writes one event. Errors from the underlying writer are
// dropped: stdout failures during a demo aren't actionable, and we
// don't want a broken pipe to take down the publisher mid-catalog.
func (f *Formatter) Emit(ev Event) {
	var line string
	if f.pretty {
		line = f.renderPretty(ev)
	} else {
		line = renderJSON(ev)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, _ = io.WriteString(f.w, line)
}

// --- Pretty rendering ---------------------------------------------------

// ANSI escape codes (8-color set; works in any terminal that respects
// the SGR escape sequence). Kept inline per the task: no third-party
// color libraries.
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
	ansiCyan   = "\x1b[36m"
	ansiBold   = "\x1b[1m"
)

// statusGlyph picks the emoji/color combination per status. The emoji
// is preserved even with NoColor — the indicator carries information
// independent of the ANSI sequence.
func statusGlyph(s Status) (emoji, color string) {
	switch s {
	case StatusOK:
		return "✅", ansiGreen
	case StatusWarn:
		return "⚠️", ansiYellow
	case StatusFail:
		return "❌", ansiRed
	case StatusInfo:
		return "ℹ️", ansiCyan
	}
	return "•", ""
}

// kindArrow returns a directional glyph for send/ack lines so the
// publisher's screen reads as a transcript. Other kinds get an empty
// string (the emoji + label carry the meaning).
func kindArrow(k Kind) string {
	switch k {
	case KindSend:
		return "→"
	case KindAck, KindAckError, KindNotification:
		return "←"
	}
	return ""
}

func (f *Formatter) colorize(c, s string) string {
	if f.noColor || c == "" {
		return s
	}
	return c + s + ansiReset
}

func (f *Formatter) renderPretty(ev Event) string {
	var b strings.Builder
	// Timestamp — bracketed for easy eyeballing.
	ts := ev.Time.Format("15:04:05")
	b.WriteString("[")
	b.WriteString(ts)
	b.WriteString("] ")

	// Status emoji (always shown — it's part of the meaning, not just
	// decoration). Color when allowed.
	emoji, color := statusGlyph(ev.Status)
	b.WriteString(f.colorize(color, emoji))
	b.WriteString(" ")

	// Optional directional arrow (send/ack). Color follows status.
	if arrow := kindArrow(ev.Kind); arrow != "" {
		b.WriteString(f.colorize(color, arrow))
		b.WriteString(" ")
	}

	// Label gets bolded so it stands out from the field cloud.
	if ev.Label != "" {
		b.WriteString(f.colorize(ansiBold, ev.Label))
		b.WriteString("  ")
	}

	// Fields — `k=v  k=v` form, two-space separator. The key is
	// dim/blue but the whole `k=v` chunk renders contiguously so an
	// operator grepping for "patient=ABC123" still finds the line.
	for i, fld := range ev.Fields {
		if i > 0 {
			b.WriteString("  ")
		}
		chunk := fld.K + "=" + fld.V
		b.WriteString(f.colorize(ansiBlue, chunk))
	}

	if ev.Msg != "" {
		if len(ev.Fields) > 0 || ev.Label != "" {
			b.WriteString("  ")
		}
		b.WriteString(f.colorize(color, ev.Msg))
	}
	b.WriteString("\n")
	return b.String()
}

// --- JSON rendering -----------------------------------------------------

// jsonEvent is the wire form for JSON Lines mode. Fields collapse
// into a flat object so callers can `jq '.fields.patient'` without
// indexing into an array.
type jsonEvent struct {
	Time   string            `json:"time"`
	Kind   string            `json:"kind"`
	Status string            `json:"status"`
	Label  string            `json:"label,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
	Msg    string            `json:"msg,omitempty"`
}

func renderJSON(ev Event) string {
	var fields map[string]string
	if len(ev.Fields) > 0 {
		fields = make(map[string]string, len(ev.Fields))
		for _, f := range ev.Fields {
			fields[f.K] = f.V
		}
	}
	je := jsonEvent{
		Time:   ev.Time.UTC().Format(time.RFC3339),
		Kind:   ev.Kind.String(),
		Status: ev.Status.String(),
		Label:  ev.Label,
		Fields: fields,
		Msg:    ev.Msg,
	}
	encoded, err := json.Marshal(je)
	if err != nil {
		// Should be unreachable for our flat shape; fall back to a
		// printable error so the operator still sees something.
		return fmt.Sprintf(`{"time":%q,"kind":"error","status":"fail","msg":%q}`+"\n",
			ev.Time.UTC().Format(time.RFC3339), err.Error())
	}
	return string(encoded) + "\n"
}
