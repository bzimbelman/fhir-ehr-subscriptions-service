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
// Determinism. Bundle JSON is encoded through fixed-shape structs so
// the byte sequence is reproducible across processes (audit B-28).
// `map[string]any` Marshal randomizes key order in Go, which would
// break the audit-log hash chain that fingerprints bundle bytes.
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
//     version-shim LLD. The `application/fhir+xml` content type is
//     rejected up front at the subscription-create / subscription-update
//     API path (handlers.requestsFHIRXML; S-12.9), so a Build call
//     should never see one. The contentType field is hardcoded to
//     ContentTypeFHIRJSON below as a result.
package builder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// ErrPermanent is the sentinel a Build error wraps when the failure is
// deterministic — i.e. the same job will fail again on every retry. The
// scheduler classifies via errors.Is(err, ErrPermanent) so it does not
// have to inspect err.Error() (OP #222). String matching is brittle: a
// log-message wording change silently broke classification before this
// sentinel landed.
var ErrPermanent = errors.New("builder: permanent error")

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

	// Hydrated carries already-fetched include resource bodies the
	// scheduler resolved via the adapter's HydrationService for
	// full-resource subscriptions (story #98 AC #2). The builder
	// appends each entry to the Bundle as a `searchEntry.mode=include`
	// resource entry after the focus body entries. Non-nil only for
	// full-resource payloads.
	Hydrated [][]byte
}

// reference is a FHIR Reference shape; only `reference` is emitted in v1.
type reference struct {
	Reference string `json:"reference"`
}

// notificationEvent is one entry inside SubscriptionStatus.notificationEvent.
// `omitempty` is used on the optional fields so the wire shape matches
// the FHIR contract exactly.
type notificationEvent struct {
	EventNumber int64      `json:"eventNumber"`
	Timestamp   string     `json:"timestamp"`
	Focus       *reference `json:"focus,omitempty"`
}

// subscriptionStatus is the FHIR R5 SubscriptionStatus resource.
type subscriptionStatus struct {
	ResourceType                 string              `json:"resourceType"`
	Status                       string              `json:"status"`
	Type                         string              `json:"type"`
	EventsSinceSubscriptionStart int64               `json:"eventsSinceSubscriptionStart"`
	Subscription                 reference           `json:"subscription"`
	Topic                        string              `json:"topic"`
	NotificationEvent            []notificationEvent `json:"notificationEvent,omitempty"`
	Error                        string              `json:"error,omitempty"`
}

// bundleEntry is a Bundle.entry. The Resource is encoded as a
// json.RawMessage so the inner ordering decisions are made once at
// emit time and never mutated by an intermediate map round-trip.
type bundleEntry struct {
	Resource json.RawMessage `json:"resource"`
}

// notificationBundle is the FHIR R5 Bundle envelope. Field order on
// the wire follows the canonical Bundle ordering: resourceType, type,
// timestamp, entry.
type notificationBundle struct {
	ResourceType string        `json:"resourceType"`
	Type         string        `json:"type"`
	Timestamp    string        `json:"timestamp"`
	Entry        []bundleEntry `json:"entry"`
}

// Build assembles a Bundle and returns the envelope ready for
// channel.Deliver.
func (b *Builder) Build(_ context.Context, job Job) (channel.NotificationEnvelope, error) {
	if job.Subscription.ID == uuid.Nil {
		return channel.NotificationEnvelope{}, fmt.Errorf("builder: subscription has nil id: %w", ErrPermanent)
	}

	// 1. Pre-sort events by per-sub event_number so the wire ordering
	//    matches the contract (Ordering invariants: notificationEvent
	//    sorted by eventNumber asc).
	//
	//    S-12: when PerSubEventNumbers is missing an entry the lookup
	//    silently returns 0 — that collapses ordering for any rows
	//    not in the map. Tie-break on the event ID so the sort is
	//    deterministic even in that pathological case.
	events := make([]repos.EhrEventRow, len(job.Events))
	copy(events, job.Events)
	sort.SliceStable(events, func(i, j int) bool {
		ni := perSubEv(job.PerSubEventNumbers, events[i].ID)
		nj := perSubEv(job.PerSubEventNumbers, events[j].ID)
		if ni != nj {
			return ni < nj
		}
		return events[i].ID.String() < events[j].ID.String()
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
	status := subscriptionStatus{
		ResourceType:                 "SubscriptionStatus",
		Status:                       string(job.Subscription.Status),
		Type:                         string(job.NotificationType),
		EventsSinceSubscriptionStart: highest,
		Subscription:                 reference{Reference: "Subscription/" + job.Subscription.ID.String()},
		Topic:                        job.Subscription.TopicURL,
	}
	if job.Subscription.Status == repos.SubError && job.Subscription.Error != "" {
		status.Error = job.Subscription.Error
	}

	if hasNotificationEvents(job.NotificationType) && len(events) > 0 {
		notifEvents := make([]notificationEvent, 0, len(events))
		payload := payloadType(job.Subscription)
		for i := range events {
			ev := &events[i]
			n := perSubEv(job.PerSubEventNumbers, ev.ID)
			entry := notificationEvent{
				EventNumber: n,
				// S-12: FHIR `instant` allows sub-second precision;
				// RFC3339 (without Nano) silently drops it. Use
				// RFC3339Nano so a millisecond-resolution
				// occurredAt survives the round trip.
				Timestamp: ev.OccurredAt.UTC().Format(time.RFC3339Nano),
			}
			// Per the contract: focus is absent for empty payloads.
			if payload != channel.PayloadEmpty {
				entry.Focus = &reference{Reference: ev.Focus}
			}
			// additionalContext is only present on full-resource and
			// only when the topic shape calls for it. Without the
			// HydrationClient wired we emit no additional references;
			// this is a deliberate v1 cut described in the package
			// docstring.
			notifEvents = append(notifEvents, entry)
		}
		status.NotificationEvent = notifEvents
	}

	// 4. Encode the SubscriptionStatus into RawMessage so its byte
	//    layout is final before it goes into the Bundle entry list.
	statusBytes, err := json.Marshal(status)
	if err != nil {
		return channel.NotificationEnvelope{}, fmt.Errorf("builder: marshal status: %w: %w", err, ErrPermanent)
	}
	entries := []bundleEntry{
		{Resource: json.RawMessage(statusBytes)},
	}

	// 5. For full-resource payloads, append focus body entries. The
	//    body's internal key order is whatever the adapter produced —
	//    we re-emit the bytes verbatim through json.RawMessage to keep
	//    canonicalization upstream of us.
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
			// Validate the body parses as JSON before embedding so a
			// corrupt resource is rejected at build time, not at
			// channel time.
			var probe json.RawMessage
			if uerr := json.Unmarshal(ev.Resource, &probe); uerr != nil {
				return channel.NotificationEnvelope{}, fmt.Errorf("builder: decode focus resource: %w: %w", uerr, ErrPermanent)
			}
			entries = append(entries, bundleEntry{Resource: probe})
		}
		// Append hydrated include resources after focus bodies. The
		// scheduler is the authority on which references to fetch
		// (story #98 AC #2 — driven by the topic's notificationShape).
		// Each entry must parse as JSON; a malformed include body is
		// a permanent build error.
		for _, body := range job.Hydrated {
			if len(body) == 0 {
				continue
			}
			var probe json.RawMessage
			if uerr := json.Unmarshal(body, &probe); uerr != nil {
				return channel.NotificationEnvelope{}, fmt.Errorf("builder: decode hydrated resource: %w: %w", uerr, ErrPermanent)
			}
			entries = append(entries, bundleEntry{Resource: probe})
		}
	}

	bundle := notificationBundle{
		ResourceType: "Bundle",
		Type:         "subscription-notification",
		// S-12: keep sub-second precision on the Bundle timestamp.
		Timestamp: b.clock().UTC().Format(time.RFC3339Nano),
		Entry:     entries,
	}

	bytes, err := json.Marshal(bundle)
	if err != nil {
		return channel.NotificationEnvelope{}, fmt.Errorf("builder: marshal bundle: %w: %w", err, ErrPermanent)
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
		// S-12: handshake / heartbeat must be deterministic so a
		// replay produces the same correlation_id. Derive a
		// stable v5 UUID from the subscription id + notification
		// type so multiple Build calls for the same logical event
		// converge.
		corr = uuid.NewSHA1(uuid.NameSpaceURL, []byte("fhir-subs:"+string(job.NotificationType)+":"+job.Subscription.ID.String())).String()
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
