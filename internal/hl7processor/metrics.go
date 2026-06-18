// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

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
