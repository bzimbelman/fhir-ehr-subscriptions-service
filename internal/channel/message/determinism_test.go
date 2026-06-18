// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package message_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/message"
)

// TestMessageBundleDeterminism wraps the same envelope 100 times and
// asserts the resulting outer Bundle bytes are byte-identical. This is
// story #59 (N-1.9): message-channel Bundle bytes must be deterministic
// so a sink-side de-dup hash (and audit-chain over chain_input) is
// stable for the same input.
//
// The non-determinism this test pins down comes from three sources in
// `wrapInMessageBundle`:
//
//  1. `time.Now()` for the outer Bundle.timestamp — must be injectable.
//  2. `uuid.NewString()` when inner Bundle.id is empty — must be
//     injectable.
//  3. `map[string]interface{}` round-trips on the inner entries —
//     re-emit verbatim through `json.RawMessage` so the embedded inner
//     entries are byte-stable.
func TestMessageBundleDeterminism(t *testing.T) {
	t.Parallel()

	// Inner Bundle without an `id` so the channel must mint one. The
	// minted id must be deterministic per the injected NewID hook.
	inner := map[string]any{
		"resourceType": "Bundle",
		"type":         "subscription-notification",
		"entry": []map[string]any{
			{
				"resource": map[string]any{
					"resourceType": "SubscriptionStatus",
					"type":         "event-notification",
				},
			},
		},
	}
	innerBytes, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}

	// Capture every POSTed body so we can compare bytes across N runs.
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Fixed clock and fixed id generator — equivalent inputs => byte
	// equivalent output.
	fixed := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	const fixedID = "00000000-0000-0000-0000-0000000000aa"

	ch, err := message.New(message.Options{
		HTTPClient:     srv.Client(),
		Metrics:        newFakeMetrics(),
		ServerEndpoint: "https://fhir-subs.example.org/facility/memorial-east",
		Clock:          func() time.Time { return fixed },
		NewID:          func() string { return fixedID },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	subID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	corr := "00000000-0000-0000-0000-000000000002"

	const N = 100
	for i := 0; i < N; i++ {
		env := channel.NotificationEnvelope{
			SubscriptionID:       subID,
			Sequence:             7,
			BundleBytes:          innerBytes,
			BundleKind:           channel.BundleEventNotification,
			PayloadType:          channel.PayloadIDOnly,
			ContentType:          channel.ContentTypeFHIRJSON,
			Attempt:              1,
			CorrelationID:        corr,
			SubscriptionEndpoint: srv.URL + "/messaging",
			Deadline:             time.Now().Add(10 * time.Second),
		}
		out, derr := ch.Deliver(context.Background(), env)
		if derr != nil {
			t.Fatalf("deliver iter %d: %v", i, derr)
		}
		if out.Kind != channel.OutcomeDelivered {
			t.Fatalf("deliver iter %d: outcome=%v reason=%q", i, out.Kind, out.Reason)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != N {
		t.Fatalf("expected %d bodies, got %d", N, len(bodies))
	}
	hashes := make(map[string]int, 1)
	for i, b := range bodies {
		sum := sha256.Sum256(b)
		hashes[hex.EncodeToString(sum[:])]++
		if i > 0 && string(b) != string(bodies[0]) {
			t.Fatalf("iter %d differs from iter 0\nfirst: %s\nthis : %s",
				i, bodies[0], b)
		}
	}
	if len(hashes) != 1 {
		t.Fatalf("expected exactly one body digest across %d wraps, got %d (%v)",
			N, len(hashes), hashes)
	}
}

// TestMessageBundleDeterminismWithInnerID covers the path where the
// inner Bundle already has an `id`, so the channel must NOT mint one.
// Result must still be byte-stable across 100 wraps.
func TestMessageBundleDeterminismWithInnerID(t *testing.T) {
	t.Parallel()

	inner := map[string]any{
		"resourceType": "Bundle",
		"id":           "abc-123",
		"type":         "subscription-notification",
		"entry": []map[string]any{
			{
				"resource": map[string]any{
					"resourceType": "SubscriptionStatus",
					"type":         "heartbeat",
				},
			},
		},
	}
	innerBytes, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fixed := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	ch, err := message.New(message.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
		Clock:      func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	subID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	const N = 100
	for i := 0; i < N; i++ {
		env := channel.NotificationEnvelope{
			SubscriptionID:       subID,
			Sequence:             7,
			BundleBytes:          innerBytes,
			BundleKind:           channel.BundleHeartbeat,
			ContentType:          channel.ContentTypeFHIRJSON,
			Attempt:              1,
			SubscriptionEndpoint: srv.URL + "/messaging",
			Deadline:             time.Now().Add(10 * time.Second),
		}
		out, derr := ch.Deliver(context.Background(), env)
		if derr != nil {
			t.Fatalf("deliver iter %d: %v", i, derr)
		}
		if out.Kind != channel.OutcomeDelivered {
			t.Fatalf("deliver iter %d: outcome=%v reason=%q", i, out.Kind, out.Reason)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(bodies); i++ {
		if string(bodies[i]) != string(bodies[0]) {
			t.Fatalf("iter %d differs from iter 0\nfirst: %s\nthis : %s",
				i, bodies[0], bodies[i])
		}
	}
}

// TestMessageBundleDeterminismMapHeavyResource exercises a focus
// resource with many fields — enough that any non-sorted map iteration
// would surface as flaky bytes. Acceptance criterion #3 calls this out
// explicitly.
func TestMessageBundleDeterminismMapHeavyResource(t *testing.T) {
	t.Parallel()

	// Build a focus resource with many keys so naive map-marshal would
	// flap. Even though Go's json.Marshal sorts map keys, the channel
	// previously did `inner.Entry []map[string]any` which causes the
	// outer Bundle to re-marshal the entries — every nested map gets
	// re-sorted, but more importantly the byte-stability story relies
	// on the entries being preserved verbatim. Including many keys
	// here ensures the canonical-form contract holds end to end.
	focus := map[string]any{
		"resourceType": "Patient",
		"id":           "patient-1",
		"meta": map[string]any{
			"versionId":   "v1",
			"lastUpdated": "2026-06-18T12:00:00Z",
			"profile":     []any{"http://hl7.org/fhir/StructureDefinition/Patient"},
		},
		"identifier": []any{
			map[string]any{"system": "urn:mrn", "value": "M-100"},
			map[string]any{"system": "urn:ssn", "value": "ZZZ-99"},
		},
		"active": true,
		"name": []any{
			map[string]any{"family": "Doe", "given": []any{"Jane", "A"}},
		},
		"gender":    "female",
		"birthDate": "1980-01-02",
		"address": []any{
			map[string]any{"city": "Anytown", "state": "CA", "postalCode": "94000"},
		},
	}
	inner := map[string]any{
		"resourceType": "Bundle",
		"id":           "inner-bundle-1",
		"type":         "subscription-notification",
		"entry": []map[string]any{
			{"resource": map[string]any{
				"resourceType": "SubscriptionStatus",
				"type":         "event-notification",
			}},
			{"resource": focus},
		},
	}
	innerBytes, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fixed := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	ch, err := message.New(message.Options{
		HTTPClient: srv.Client(),
		Metrics:    newFakeMetrics(),
		Clock:      func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	subID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	const N = 100
	for i := 0; i < N; i++ {
		env := channel.NotificationEnvelope{
			SubscriptionID:       subID,
			Sequence:             7,
			BundleBytes:          innerBytes,
			BundleKind:           channel.BundleEventNotification,
			ContentType:          channel.ContentTypeFHIRJSON,
			Attempt:              1,
			SubscriptionEndpoint: srv.URL + "/messaging",
			Deadline:             time.Now().Add(10 * time.Second),
		}
		out, derr := ch.Deliver(context.Background(), env)
		if derr != nil {
			t.Fatalf("deliver iter %d: %v", i, derr)
		}
		if out.Kind != channel.OutcomeDelivered {
			t.Fatalf("deliver iter %d: outcome=%v reason=%q", i, out.Kind, out.Reason)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(bodies); i++ {
		if string(bodies[i]) != string(bodies[0]) {
			t.Fatalf("map-heavy iter %d differs from iter 0\nfirst: %s\nthis : %s",
				i, bodies[0], bodies[i])
		}
	}
}
