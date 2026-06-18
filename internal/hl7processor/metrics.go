// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

// Metric cardinality contract:
//
// MetricResourceChangesTotal carries `adapter_id × resource_type ×
// change_kind`. Each label has a closed, deployment-bound domain:
//   - adapter_id is set once at startup from `Config.AdapterID` and is
//     constant for the process lifetime; one cluster typically runs
//     one or two adapters total.
//   - resource_type is the FHIR R5 resource type; the SPI restricts
//     this to the bundled vocabulary (Patient, Observation, ...).
//   - change_kind is `create | update | delete | merge` (the SPI's
//     ChangeKind enum, fixed-domain).
//
// The 3-way cross product is therefore O(adapters × resources × 4) ≈
// O(40) for realistic deployments. No user-supplied input flows into
// any of the labels; cardinality is bounded at registry registration
// time. The audit's N-1 cardinality concern is documented here rather
// than capped at runtime — adding a cap would mask a misconfigured
// adapter_id rather than fix it (N-1).

// Metric names emitted by the processor. Wire form per LLD §8.
const (
	MetricMessagesProcessed    = "fhir_subs_hl7processor_messages_processed"
	MetricProcessingDuration   = "fhir_subs_hl7processor_processing_duration"
	MetricDeadLetteredTotal    = "fhir_subs_hl7processor_dead_lettered_total"
	MetricPairsHeld            = "fhir_subs_hl7processor_pairs_held"
	MetricPairsResolved        = "fhir_subs_hl7processor_pairs_resolved"
	MetricPairsExpired         = "fhir_subs_hl7processor_pairs_expired"
	MetricResourceChangesTotal = "fhir_subs_hl7processor_resource_changes_total"
	MetricDeadLettersTotal     = "fhir_subs_hl7processor_dead_letters_total"
	MetricCancelReplacePending = "fhir_subs_hl7processor_cancel_replace_pending"
	MetricStageDurationSeconds = "fhir_subs_hl7processor_stage_duration_seconds"
	// MetricClaimCycleErrors is bumped once per claim/reaper iteration
	// that returned a non-canceled error. Operators want to alert on
	// repeated failures (S-9.7).
	MetricClaimCycleErrors = "fhir_subs_hl7processor_claim_cycle_errors"
	// MetricSameKindCollision is bumped when a same-kind pair shows up
	// under the same correlation key — indicates a missed
	// cancellation/replacement somewhere upstream (S-9.11).
	MetricSameKindCollision = "fhir_subs_hl7processor_same_kind_collision"
)

// Outcome label values for [MetricMessagesProcessed].
const (
	OutcomeEmitted    = "emitted"
	OutcomeHeld       = "held"
	OutcomeResolved   = "resolved"
	OutcomeRolledBack = "rolled_back"
	OutcomeDeadLetter = "dead_letter"
)

// MetricsEmitter is the metrics seam between the processor and the host's
// observability stack. Same shape as [internal/mllp.MetricsEmitter] so a
// single host emitter implementation services both packages.
type MetricsEmitter interface {
	// Inc bumps a counter by 1 with the given labels.
	Inc(name string, labels map[string]string)
	// Add bumps a counter by delta with the given labels.
	Add(name string, delta float64, labels map[string]string)
	// Observe records a histogram observation with the given labels.
	Observe(name string, value float64, labels map[string]string)
	// Set sets a gauge value with the given labels.
	Set(name string, value float64, labels map[string]string)
}

// nopMetrics is the no-op default; keeps internals nil-safe.
type nopMetrics struct{}

func (nopMetrics) Inc(string, map[string]string)              {}
func (nopMetrics) Add(string, float64, map[string]string)     {}
func (nopMetrics) Observe(string, float64, map[string]string) {}
func (nopMetrics) Set(string, float64, map[string]string)     {}
