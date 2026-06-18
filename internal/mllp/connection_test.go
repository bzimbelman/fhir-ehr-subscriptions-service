// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
)

// B-30: When the configured AllowedMessageTypes list rejects a frame, the
// nack metric MUST NOT use the rejected MSH-9 string verbatim as a label
// value. Hostile peers blasting garbage MSH-9 values would otherwise
// create a new Prometheus time-series per value.
//
// Contract: the "type" label is whitelisted to the configured set (so
// known types still split out for ops triage), and any value outside the
// allowlist is bucketed to "other".
func TestNackTotal_TypeLabel_BucketsUnknownToOther(t *testing.T) {
	p := &fakePersister{}
	m := newFakeMetrics()
	ep := EndpointConfig{
		Name:                "adt-feed",
		AllowedMessageTypes: []string{"ADT", "ORU"},
	}
	cfg := defaultConfig(ep)

	server, client := net.Pipe()
	defer client.Close()

	go HandleConnection(context.Background(), server, ep, cfg, p, m, "127.0.0.1:5050")

	// Send 100 frames with distinct, hostile MSH-9 values. None of these
	// are in the allowlist, so they all NACK.
	for i := 0; i < 100; i++ {
		body := fmt.Sprintf("MSH|^~\\&|S|F|||20240101||EVIL%04d^X%04d|GARBAGE-%d|P|2.5\rPID|||x\r", i, i, i)
		if _, err := client.Write(frameBytes(body)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		ack := readFrame(t, client)
		if !strings.Contains(string(ack), "MSA|AE|") {
			t.Fatalf("expected NACK for frame %d; got %q", i, ack)
		}
	}

	// Drain by closing client; allow handler goroutine to finish.
	_ = client.Close()

	// Inspect the recorded counters: every nack_total counter labeled
	// reason=message_type must use a "type" label drawn from the
	// allowlist OR the literal "other" bucket. The audit's contract says
	// "exactly one bucket for unknown types" — so we expect *exactly one*
	// counter key labeled type=other (carrying all 100 increments).
	keys := m.allCounterKeys()
	otherSeries := 0
	leakedSeries := 0
	for _, k := range keys {
		// Only consider nack_total reason=message_type series.
		if !strings.Contains(k, MetricNackTotal) {
			continue
		}
		if !strings.Contains(k, "reason=message_type") {
			continue
		}
		// Any "type=" must be in the allowlist or "other".
		if strings.Contains(k, "type=other") {
			otherSeries++
			continue
		}
		// Inspect known-good types: split out type=<value>.
		// Allow type=ADT or type=ORU (in case real allowed types ever
		// reach this branch, though our test inputs are all unknown).
		if strings.Contains(k, "type=ADT") || strings.Contains(k, "type=ORU") {
			continue
		}
		leakedSeries++
		t.Logf("leaked metric series: %s", k)
	}
	if otherSeries != 1 {
		t.Fatalf("expected exactly 1 nack_total series with type=other; got %d", otherSeries)
	}
	if leakedSeries != 0 {
		t.Fatalf("hostile MSH-9 values leaked into metric labels: %d unauthorized series", leakedSeries)
	}

	// Confirm the bucket counter actually accumulated all 100 increments.
	bucket := m.counter(MetricNackTotal, map[string]string{
		"listener_endpoint": ep.Name,
		"reason":            "message_type",
		"type":              "other",
	})
	if bucket != 100 {
		t.Fatalf("expected nack_total{type=other}=100; got %v", bucket)
	}
}
