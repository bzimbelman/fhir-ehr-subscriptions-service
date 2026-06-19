// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package message_test

import (
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/message"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
)

// TestMessageChannel_RegistersInScheduler pins AC for story #103:
// "Unit test asserting Lookup('message') returns the registered channel
// after wiring." Constructs the message channel and registers it in a
// MapRegistry directly so the assertion does not require a Postgres
// pool — production wiring uses the same MapRegistry shape.
func TestMessageChannel_RegistersInScheduler(t *testing.T) {
	t.Parallel()

	mc, err := message.New(message.Options{ServerEndpoint: "https://example/m"})
	if err != nil {
		t.Fatalf("message.New: %v", err)
	}
	reg := scheduler.NewMapRegistry()
	reg.Register("message", mc)

	got, ok := reg.Lookup("message")
	if !ok {
		t.Fatalf("Lookup(message) ok=false; want registered")
	}
	if got == nil {
		t.Fatalf("Lookup(message) channel is nil")
	}
	// Close MUST be implemented per the extended Channel SPI.
	if cerr := got.Close(); cerr != nil {
		t.Errorf("Close: unexpected error %v", cerr)
	}
}
