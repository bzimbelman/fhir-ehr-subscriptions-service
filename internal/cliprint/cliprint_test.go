// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package cliprint

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 1, 2, 14, 1, 2, 0, time.UTC)

func TestPrettyFormatter_OK_RendersGreenCheckmark(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := NewFormatter(&buf, Options{Pretty: true, NoColor: false})
	f.Emit(Event{
		Time:   fixedTime,
		Kind:   KindSend,
		Status: StatusOK,
		Label:  "ORU^R01",
		Fields: []Field{{K: "patient", V: "ABC123"}, {K: "obs", V: "718-7"}},
		Msg:    "sent",
	})

	out := buf.String()
	if !strings.Contains(out, "[14:01:02]") {
		t.Errorf("missing timestamp: %q", out)
	}
	if !strings.Contains(out, "✅") {
		t.Errorf("OK status should render checkmark emoji: %q", out)
	}
	if !strings.Contains(out, "ORU^R01") {
		t.Errorf("missing label: %q", out)
	}
	if !strings.Contains(out, "patient=ABC123") {
		t.Errorf("missing patient field: %q", out)
	}
	if !strings.Contains(out, "obs=718-7") {
		t.Errorf("missing obs field: %q", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("colorize on but no ANSI: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("must end with newline: %q", out)
	}
}

func TestPrettyFormatter_Warn_RendersWarningEmoji(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := NewFormatter(&buf, Options{Pretty: true, NoColor: true})
	f.Emit(Event{
		Time:   fixedTime,
		Kind:   KindAck,
		Status: StatusWarn,
		Label:  "ACK",
		Fields: []Field{{K: "control_id", V: "DEMO0001"}},
		Msg:    "AE (application error)",
	})

	out := buf.String()
	if !strings.Contains(out, "⚠️") {
		t.Errorf("Warn should render warning emoji: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("NoColor=true but ANSI present: %q", out)
	}
}

func TestPrettyFormatter_Fail_RendersFailEmoji(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := NewFormatter(&buf, Options{Pretty: true, NoColor: true})
	f.Emit(Event{
		Time:   fixedTime,
		Kind:   KindAckError,
		Status: StatusFail,
		Label:  "ACK",
		Fields: []Field{{K: "control_id", V: "DEMO0001"}},
		Msg:    "FAILED: dial tcp: connection refused",
	})

	out := buf.String()
	if !strings.Contains(out, "❌") {
		t.Errorf("Fail should render fail emoji: %q", out)
	}
	if !strings.Contains(out, "FAILED: dial tcp: connection refused") {
		t.Errorf("missing failure message: %q", out)
	}
}

func TestPrettyFormatter_NoColorEnvVar_SuppressesAnsi(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	// Caller passes NoColor:false but env should still suppress.
	f := NewFormatter(&buf, Options{Pretty: true, NoColor: false})
	f.Emit(Event{Time: fixedTime, Kind: KindInfo, Status: StatusInfo, Msg: "listening"})

	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("NO_COLOR set but ANSI present: %q", out)
	}
}

func TestJSONFormatter_EmitsOneLineJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := NewFormatter(&buf, Options{Pretty: false})
	f.Emit(Event{
		Time:   fixedTime,
		Kind:   KindNotification,
		Status: StatusOK,
		Label:  "lab-results",
		Fields: []Field{{K: "patient", V: "Patient/ABC"}, {K: "event", V: "7"}},
	})

	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("JSON output must be newline-terminated: %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("expected exactly one line; got: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("JSON output must never carry ANSI: %q", out)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got := parsed["kind"]; got != "notification" {
		t.Errorf(`kind: got %v, want "notification"`, got)
	}
	if got := parsed["status"]; got != "ok" {
		t.Errorf(`status: got %v, want "ok"`, got)
	}
	if got := parsed["label"]; got != "lab-results" {
		t.Errorf(`label: got %v, want "lab-results"`, got)
	}
	if got := parsed["time"]; got != "2026-01-02T14:01:02Z" {
		t.Errorf(`time: got %v, want RFC3339 UTC`, got)
	}
	fields, ok := parsed["fields"].(map[string]any)
	if !ok {
		t.Fatalf("fields not an object: %v", parsed["fields"])
	}
	if fields["patient"] != "Patient/ABC" {
		t.Errorf("fields.patient: got %v", fields["patient"])
	}
}

func TestJSONFormatter_OmitsEmptyOptional(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := NewFormatter(&buf, Options{Pretty: false})
	f.Emit(Event{Time: fixedTime, Kind: KindInfo, Status: StatusInfo, Msg: "ready"})

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if _, has := parsed["label"]; has {
		t.Errorf("empty label should be omitted from JSON: %v", parsed)
	}
	if _, has := parsed["fields"]; has {
		t.Errorf("nil fields should be omitted from JSON: %v", parsed)
	}
}

func TestPrettyFormatter_ConcurrentEmitsDoNotInterleave(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := NewFormatter(&buf, Options{Pretty: true, NoColor: true})
	const N = 50
	done := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		go func() {
			f.Emit(Event{Time: fixedTime, Kind: KindSend, Status: StatusOK, Label: "X", Msg: "sent"})
			done <- struct{}{}
		}()
	}
	for i := 0; i < N; i++ {
		<-done
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != N {
		t.Fatalf("expected %d lines, got %d:\n%s", N, len(lines), buf.String())
	}
	for i, l := range lines {
		if !strings.Contains(l, "[14:01:02]") {
			t.Errorf("line %d malformed (interleaved write?): %q", i, l)
		}
	}
}

func TestPrettyFormatter_StatusInfo_RendersInfoIcon(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := NewFormatter(&buf, Options{Pretty: true, NoColor: true})
	f.Emit(Event{Time: fixedTime, Kind: KindInfo, Status: StatusInfo, Msg: "subscribed: id=abc"})

	out := buf.String()
	if !strings.Contains(out, "ℹ️") {
		t.Errorf("Info status should render info icon: %q", out)
	}
	if !strings.Contains(out, "subscribed: id=abc") {
		t.Errorf("missing info message: %q", out)
	}
}

// JSON output for Kind/Status round-trips through the canonical strings.
func TestKindStatusJSONStrings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		k    Kind
		want string
	}{
		{KindSend, "send"},
		{KindAck, "ack"},
		{KindAckError, "ack_error"},
		{KindNotification, "notification"},
		{KindInfo, "info"},
		{KindError, "error"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("Kind(%d).String() = %q; want %q", c.k, got, c.want)
		}
	}

	statuses := []struct {
		s    Status
		want string
	}{
		{StatusOK, "ok"},
		{StatusWarn, "warn"},
		{StatusFail, "fail"},
		{StatusInfo, "info"},
	}
	for _, c := range statuses {
		if got := c.s.String(); got != c.want {
			t.Errorf("Status(%d).String() = %q; want %q", c.s, got, c.want)
		}
	}
}
