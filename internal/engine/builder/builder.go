// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package builder is Stage 4 of the pipeline: it assembles the
// subscription-notification Bundle the channel will deliver. It owns
// the wire shape (per docs/high-level-design/contracts/notification-bundle.md
// and the FHIR R5 SubscriptionStatus spec); it does NOT own row writes,
// retry, hydration, or channel I/O.
//
// One Build call → one NotificationEnvelope. The envelope carries
// already-serialized bytes; channels do not parse or re-serialize.
//
// What the builder does not do (yet):
//   - hydrate referenced resources from the adapter Hydration Service.
//     The full-resource path here emits the focus resource(s) only;
//     wiring _include / _revinclude through the topic.notificationShape
//     hint requires the HydrationClient SPI which is owned by the
//     adapter integration. The current implementation is correct for
//     the subset that exercises the Stage 4 wire shape and the LLD's
//     "build_full_resource_entries" pseudocode WITHOUT hydration; a
//     follow-up patch will plumb the HydrationClient.
//   - serialize XML. The envelope's content type is honored only for
//     application/fhir+json in v1; XML is deferred behind the
//     version-shim LLD.
package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// Config tunes the builder.
type Config struct {
	// Clock is the time source for Bundle.timestamp. nil → time.Now.
	Clock func() time.Time
	// DefaultTimeout is the per-attempt deadline applied to the
	// envelope when the subscription does not specify one. The LLD
	// suggests 30s.
	DefaultTimeout time.Duration
}

// Builder is the Stage 4 component.
type Builder struct {
	clock          func() time.Time
	defaultTimeout time.Duration
}

// New constructs a Builder.
func New(cfg Config) *Builder {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	return &Builder{clock: cfg.Clock, defaultTimeout: cfg.DefaultTimeout}
}

// Job is the input to Build. The scheduler constructs one per
// dispatch (single-event or batched).
type Job struct {
	Subscription     repos.SubscriptionRow
	NotificationType channel.BundleKind
	// Events is one ehr_events row per event in the batch. Empty for
	// handshake / heartbeat / query-status. >=1 for event-notification
	// and query-event.
	Events []repos.EhrEventRow
	// PerSubEventNumbers maps EhrEventRow.ID → the per-subscription
	// event_number (deliveries.event_number, computed by the
	// submatcher's max+1 rule per ADR 0010 #2). Required for
	// event-notification and query-event; ignored for handshake /
	// heartbeat.
	PerSubEventNumbers map[uuid.UUID]int64
	// Attempt is the delivery attempt counter (0 for first), copied
	// onto the envelope so the channel can include it as
	// X-Attempt or equivalent.
	Attempt uint32
	// CorrelationIDOverride lets the scheduler pin the envelope's
	// correlation_id (e.g., for handshake which has no underlying
	// event). Empty falls back to the first event's correlation, then
	// to a fresh UUID.
	CorrelationIDOverride string
}

// Build assembles a Bundle and returns the envelope ready for
// channel.Deliver.
func (b *Builder) Build(_ context.Context, job Job) (channel.NotificationEnvelope, error) {
	if job.Subscription.ID == uuid.Nil {
		return channel.NotificationEnvelope{}, fmt.Errorf("builder: subscription has nil id")
	}

	// 1. Pre-sort events by per-sub event_number so the wire ordering
	//    matches the contract (Ordering invariants: notificationEvent
	//    sorted by eventNumber asc).
	events := make([]repos.EhrEventRow, len(job.Events))
	copy(events, job.Events)
	sort.SliceStable(events, func(i, j int) bool {
		return perSubEv(job.PerSubEventNumbers, events[i].ID) <
			perSubEv(job.PerSubEventNumbers, events[j].ID)
	})

	// 2. Compute eventsSinceSubscriptionStart.
	highest := job.Subscription.EventsSinceSubscriptionStart
	for i := range events {
		n := perSubEv(job.PerSubEventNumbers, events[i].ID)
		if n > highest {
			highest = n
		}
	}

	// 3. Build SubscriptionStatus.
	status := map[string]any{
		"resourceType":                 "SubscriptionStatus",
		"status":                       string(job.Subscription.Status),
		"type":                         string(job.NotificationType),
		"eventsSinceSubscriptionStart": highest,
		"subscription":                 map[string]any{"reference": "Subscription/" + job.Subscription.ID.String()},
		"topic":                        job.Subscription.TopicURL,
	}
	if job.Subscription.Status == repos.SubError && job.Subscription.Error != "" {
		status["error"] = job.Subscription.Error
	}

	if hasNotificationEvents(job.NotificationType) && len(events) > 0 {
		notifEvents := make([]map[string]any, 0, len(events))
		for i := range events {
			ev := &events[i]
			n := perSubEv(job.PerSubEventNumbers, ev.ID)
			entry := map[string]any{
				"eventNumber": n,
				"timestamp":   ev.OccurredAt.UTC().Format(time.RFC3339),
			}
			payload := payloadType(job.Subscription)
			// Per the contract: focus is absent for empty payloads.
			if payload != channel.PayloadEmpty {
				entry["focus"] = map[string]any{"reference": ev.Focus}
			}
			// additionalContext is only present on full-resource and
			// only when the topic shape calls for it. Without the
			// HydrationClient wired we emit no additional references;
			// this is a deliberate v1 cut described in the package
			// docstring.
			notifEvents = append(notifEvents, entry)
		}
		status["notificationEvent"] = notifEvents
	}

	// 4. Build the Bundle skeleton. SubscriptionStatus is always
	//    entry index 0.
	entries := []map[string]any{
		{"resource": status},
	}

	// 5. For full-resource payloads, append focus body entries.
	if hasNotificationEvents(job.NotificationType) && payloadType(job.Subscription) == channel.PayloadFullResource {
		seen := make(map[string]struct{}, len(events))
		for i := range events {
			ev := &events[i]
			if _, dup := seen[ev.Focus]; dup {
				continue
			}
			seen[ev.Focus] = struct{}{}
			if len(ev.Resource) == 0 {
				continue
			}
			var body map[string]any
			if err := json.Unmarshal(ev.Resource, &body); err != nil {
				return channel.NotificationEnvelope{}, fmt.Errorf("builder: decode focus resource: %w", err)
			}
			entries = append(entries, map[string]any{"resource": body})
		}
	}

	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "subscription-notification",
		"timestamp":    b.clock().UTC().Format(time.RFC3339),
		"entry":        entries,
	}

	bytes, err := json.Marshal(bundle)
	if err != nil {
		return channel.NotificationEnvelope{}, fmt.Errorf("builder: marshal bundle: %w", err)
	}

	// 6. Resolve content type. Defaults to fhir+json.
	contentType := channel.ContentTypeFHIRJSON

	// 7. Resolve envelope deadline.
	timeout := b.defaultTimeout
	if job.Subscription.Timeout != nil && *job.Subscription.Timeout > 0 {
		timeout = *job.Subscription.Timeout
	}

	// 8. Resolve correlation id.
	corr := job.CorrelationIDOverride
	if corr == "" && len(events) > 0 {
		corr = events[0].CorrelationID.String()
	}
	if corr == "" {
		corr = uuid.NewString()
	}

	seq := uint64(0)
	if highest > 0 {
		seq = uint64(highest)
	}
	env := channel.NotificationEnvelope{
		SubscriptionID:       job.Subscription.ID,
		Sequence:             seq,
		BundleBytes:          bytes,
		BundleKind:           job.NotificationType,
		PayloadType:          payloadType(job.Subscription),
		ContentType:          contentType,
		Attempt:              job.Attempt,
		CorrelationID:        corr,
		SubscriptionEndpoint: job.Subscription.Endpoint,
		Deadline:             b.clock().Add(timeout),
	}
	return env, nil
}

// hasNotificationEvents returns true for Bundle kinds that carry one or
// more notificationEvent entries. handshake / heartbeat /
// query-status do not.
func hasNotificationEvents(k channel.BundleKind) bool {
	switch k {
	case channel.BundleEventNotification, channel.BundleQueryEvent:
		return true
	}
	return false
}

// payloadType maps the subscription's content string onto the typed
// channel.PayloadType.
func payloadType(sub repos.SubscriptionRow) channel.PayloadType {
	switch sub.Content {
	case "empty":
		return channel.PayloadEmpty
	case "id-only", "":
		return channel.PayloadIDOnly
	case "full-resource":
		return channel.PayloadFullResource
	default:
		return channel.PayloadIDOnly
	}
}

// perSubEv looks up the per-subscription event_number for the given
// ehr_events row id. Defaults to 0 when missing — callers are expected
// to populate the map for every event they pass in.
func perSubEv(m map[uuid.UUID]int64, id uuid.UUID) int64 {
	if m == nil {
		return 0
	}
	return m[id]
}
