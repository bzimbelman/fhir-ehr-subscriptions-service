// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/mockehr"
)

// publisher walks a Catalog and emits each MessageEntry over MLLP, printing
// a "→ send" / "← ack" line per message in the style the demo doc shows.
//
// Fields nowFn / idFn / noColor are seams so tests can pin output and so an
// operator can pipe stdout through a non-tty without ANSI codes leaking.
type publisher struct {
	addr    string
	out     io.Writer
	nowFn   func() time.Time
	idFn    func() string
	noColor bool
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
	client := mockehr.NewMLLPClient(p.addr)
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
func (p *publisher) sendOne(ctx context.Context, client *mockehr.MLLPClient, entry MessageEntry) error {
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

// printSend writes the "→ TRIGGER  details                sent" line.
func (p *publisher) printSend(entry MessageEntry, ctrlID string) {
	ts := p.nowFn().Format("15:04:05")
	trigger := triggerLabel(entry.Template)
	details := summary(entry)
	desc := ""
	if entry.Description != "" {
		desc = "  " + entry.Description
	}
	prefix := p.colorize(colorBlue, "→")
	line := fmt.Sprintf("[%s] %s %-10s %-50s sent%s\n", ts, prefix, trigger, details+" id="+ctrlID, desc)
	_, _ = io.WriteString(p.out, line)
}

// printAck writes the "← ACK control_id=…  OK/NACK" line. The result is
// classified by inspecting MSA-1 for AA/AE/AR.
func (p *publisher) printAck(ctrlID string, ack []byte) {
	ts := p.nowFn().Format("15:04:05")
	status := "OK"
	color := colorGreen
	switch ackKind := classifyACK(ack); ackKind {
	case "AA":
		status = "OK"
		color = colorGreen
	case "AE":
		status = "AE (application error)"
		color = colorYellow
	case "AR":
		status = "AR (application reject)"
		color = colorRed
	default:
		status = "UNKNOWN"
		color = colorYellow
	}
	prefix := p.colorize(colorGreen, "←")
	statusOut := p.colorize(color, status)
	line := fmt.Sprintf("[%s] %s %-10s control_id=%-20s %s\n", ts, prefix, "ACK", ctrlID, statusOut)
	_, _ = io.WriteString(p.out, line)
}

// printAckError writes a red "← ACK …  FAILED: <err>" line on transport
// failures (dial / write / read errors before any MSA could be parsed).
func (p *publisher) printAckError(ctrlID string, err error) {
	ts := p.nowFn().Format("15:04:05")
	prefix := p.colorize(colorRed, "←")
	statusOut := p.colorize(colorRed, "FAILED: "+err.Error())
	line := fmt.Sprintf("[%s] %s %-10s control_id=%-20s %s\n", ts, prefix, "ACK", ctrlID, statusOut)
	_, _ = io.WriteString(p.out, line)
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

// colorize wraps s in an ANSI escape unless noColor is set. The constants
// stay private — operators don't reach in and override them.
type ansi string

const (
	colorReset  ansi = "\033[0m"
	colorRed    ansi = "\033[31m"
	colorGreen  ansi = "\033[32m"
	colorYellow ansi = "\033[33m"
	colorBlue   ansi = "\033[34m"
)

func (p *publisher) colorize(c ansi, s string) string {
	if p.noColor {
		return s
	}
	return string(c) + s + string(colorReset)
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
