// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/cliprint"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/demosupport/mllp"
)

// publisher walks a Catalog and emits each MessageEntry over MLLP,
// printing a "→ send" / "← ack" line per message in the style the
// demo doc shows. Output goes through cliprint.Formatter so the same
// stream can render as a colored transcript or as JSON Lines.
//
// nowFn / idFn are seams so tests can pin output deterministically.
type publisher struct {
	addr  string
	fmt   *cliprint.Formatter
	nowFn func() time.Time
	idFn  func() string
}

// run sends every message in cat in order. It returns the first send error
// or context cancellation; success returns nil.
//
// Between messages it sleeps for the entry's Delay (zero means "no sleep",
// useful in tests). The MLLP send itself is bound by an inner timeout so a
// hung listener doesn't block the operator forever.
func (p *publisher) run(ctx context.Context, cat *Catalog) error {
	if p.nowFn == nil {
		p.nowFn = time.Now
	}
	if p.idFn == nil {
		p.idFn = defaultIDGen()
	}
	client := mllp.NewClient(p.addr)
	for i, entry := range cat.Messages {
		if entry.Delay > 0 {
			select {
			case <-time.After(entry.Delay):
			case <-ctx.Done():
				return fmt.Errorf("publisher: context done before message %d: %w", i, ctx.Err())
			}
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("publisher: context done at message %d: %w", i, err)
		}
		if err := p.sendOne(ctx, client, entry); err != nil {
			return fmt.Errorf("publisher: message %d (%s): %w", i, entry.Description, err)
		}
	}
	return nil
}

// sendOne builds, sends, and prints one entry. It is the unit operators
// see in the terminal: arrival lines flush before the next entry begins.
func (p *publisher) sendOne(ctx context.Context, client *mllp.Client, entry MessageEntry) error {
	ctrlID := p.idFn()
	body, _, err := buildMessage(entry, ctrlID)
	if err != nil {
		return err
	}

	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	p.printSend(entry, ctrlID)
	ack, err := client.Send(sendCtx, []byte(body))
	if err != nil {
		p.printAckError(ctrlID, err)
		return err
	}
	p.printAck(ctrlID, ack)
	return nil
}

// printSend emits a cliprint "send" event for one outgoing message.
// The trigger (ORU^R01 etc.) becomes the label; patient/obs/value
// derived from the catalog summary become structured fields.
func (p *publisher) printSend(entry MessageEntry, ctrlID string) {
	fields := summaryFields(entry)
	fields = append(fields, cliprint.Field{K: "id", V: ctrlID})
	if entry.Description != "" {
		fields = append(fields, cliprint.Field{K: "desc", V: entry.Description})
	}
	p.fmt.Emit(cliprint.Event{
		Time:   p.nowFn(),
		Kind:   cliprint.KindSend,
		Status: cliprint.StatusOK,
		Label:  triggerLabel(entry.Template),
		Fields: fields,
		Msg:    "sent",
	})
}

// printAck emits a cliprint "ack" event. AA → OK, AE → warn, AR/UNKNOWN
// → fail; the publisher keeps running either way.
func (p *publisher) printAck(ctrlID string, ack []byte) {
	var (
		status cliprint.Status
		msg    string
	)
	switch classifyACK(ack) {
	case "AA":
		status = cliprint.StatusOK
		msg = "OK"
	case "AE":
		status = cliprint.StatusWarn
		msg = "AE (application error)"
	case "AR":
		status = cliprint.StatusFail
		msg = "AR (application reject)"
	default:
		status = cliprint.StatusWarn
		msg = "UNKNOWN"
	}
	p.fmt.Emit(cliprint.Event{
		Time:   p.nowFn(),
		Kind:   cliprint.KindAck,
		Status: status,
		Label:  "ACK",
		Fields: []cliprint.Field{{K: "control_id", V: ctrlID}},
		Msg:    msg,
	})
}

// printAckError emits a cliprint "ack_error" event on transport
// failures (dial / write / read before any MSA could be parsed).
func (p *publisher) printAckError(ctrlID string, err error) {
	p.fmt.Emit(cliprint.Event{
		Time:   p.nowFn(),
		Kind:   cliprint.KindAckError,
		Status: cliprint.StatusFail,
		Label:  "ACK",
		Fields: []cliprint.Field{{K: "control_id", V: ctrlID}},
		Msg:    "FAILED: " + err.Error(),
	})
}

// classifyACK looks for "MSA|<code>|" and returns "AA" / "AE" / "AR".
// Anything else returns "" so callers can render it as UNKNOWN.
func classifyACK(ack []byte) string {
	idx := bytes.Index(ack, []byte("MSA|"))
	if idx < 0 {
		return ""
	}
	rest := ack[idx+4:]
	end := bytes.IndexByte(rest, '|')
	if end < 0 {
		return ""
	}
	code := strings.ToUpper(strings.TrimSpace(string(rest[:end])))
	switch code {
	case "AA", "AE", "AR":
		return code
	default:
		return ""
	}
}

// summaryFields returns the structured field set rendered in the
// publisher's send line. Field selection mirrors the demo doc's
// example output (patient + content highlights).
func summaryFields(e MessageEntry) []cliprint.Field {
	switch e.Template {
	case "oru-r01":
		return []cliprint.Field{
			{K: "patient", V: e.Fields["patient_id"]},
			{K: "obs", V: e.Fields["observation_code"]},
			{K: "value", V: e.Fields["value"]},
		}
	case "adt-a01":
		out := []cliprint.Field{{K: "patient", V: e.Fields["patient_id"]}}
		if fac := e.Fields["facility"]; fac != "" {
			out = append(out, cliprint.Field{K: "facility", V: fac})
		}
		return out
	default:
		return []cliprint.Field{{K: "patient", V: e.Fields["patient_id"]}}
	}
}

// defaultIDGen returns a goroutine-safe-ish (single-publisher) generator
// that mints "DEMO-<unix-ns>-<n>"-shaped MSH-10 ids. The publisher is
// single-goroutine so a counter without a mutex is fine here.
func defaultIDGen() func() string {
	start := time.Now().UnixNano()
	var n int
	return func() string {
		n++
		return fmt.Sprintf("DEMO-%d-%d", start, n)
	}
}
