// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
)

// TestFormatRunError_D4_UnknownAdapterStructured asserts that when the
// production runtime fails because the configured adapter.id is not in
// the bundled registry, the operator-facing error is structured (mentions
// the requested id, the bundled list, and a fixed prefix the error
// scraping tool can grep for) rather than the raw fmt.Errorf chain.
//
// D-4.
func TestFormatRunError_D4_UnknownAdapterStructured(t *testing.T) {
	t.Parallel()

	// Mirror the wrapping done by buildProductionRuntime + runWithHooks
	// so the test exercises the real chain.
	inner := &registry.UnknownAdapterError{
		Requested: "epic-foo",
		Bundled:   []string{"default", "ge"},
	}
	wrapped := fmt.Errorf("production wiring: %w", fmt.Errorf("adapter load: %w", inner))

	got := formatRunError(wrapped)
	if !strings.Contains(got, "unknown adapter") {
		t.Errorf("missing 'unknown adapter' marker; got %q", got)
	}
	if !strings.Contains(got, `"epic-foo"`) {
		t.Errorf("missing requested id; got %q", got)
	}
	if !strings.Contains(got, "default") || !strings.Contains(got, "ge") {
		t.Errorf("missing bundled list; got %q", got)
	}
	if !strings.HasPrefix(got, "error: adapter ") {
		t.Errorf("expected 'error: adapter ' prefix; got %q", got)
	}
}

// TestFormatRunError_D4_OtherErrorsUnchanged asserts that errors
// unrelated to the adapter load path are formatted with the legacy
// "error: run: <err>" prefix so the new structured branch does not
// bury other failure modes.
//
// D-4.
func TestFormatRunError_D4_OtherErrorsUnchanged(t *testing.T) {
	t.Parallel()

	got := formatRunError(errors.New("something blew up"))
	if got != "error: run: something blew up" {
		t.Errorf("got %q, want 'error: run: something blew up'", got)
	}
}
