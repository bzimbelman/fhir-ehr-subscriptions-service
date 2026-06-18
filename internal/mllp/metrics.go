// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

// MetricsEmitter is the metrics seam between the listener and the host's
// observability stack. The listener emits typed events; an adapter at the
// host translates them to Prometheus, OTLP, or whatever the deployment
// wires up. Tests use a fake.
//
// All counter / histogram names below carry the `fhir_subs_` prefix when
// rendered to the wire by the host adapter (per ADR 0010's metric naming
// rule). The listener emits the unprefixed event name and labels.
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

// Metric event names emitted by the listener. The host adapter renders
// these as `fhir_subs_<name>` on the wire.
const (
	MetricMessagesReceivedTotal = "hl7_messages_received_total"
	MetricMessagesAckedTotal    = "hl7_messages_acked_total"
	MetricMessageBytes          = "hl7_message_bytes"
	MetricMalformedTotal        = "hl7_malformed_total"
	MetricNackTotal             = "hl7_nack_total"
	MetricPersistDurationMS     = "hl7_persist_duration_ms"
	MetricActiveConnections     = "hl7_active_connections"
	MetricInflightPerConnection = "hl7_inflight_per_connection"
	MetricAcceptErrorsTotal     = "hl7_accept_errors_total"
	MetricReadErrorsTotal       = "hl7_read_errors_total"
	MetricDisconnectMidFrame    = "hl7_disconnect_mid_frame_total"
	MetricDropForPersistFails   = "hl7_drop_for_persist_failures_total"
)

// Outcome label values for MetricMessagesAckedTotal.
const (
	OutcomeAA = "AA" // application accept
	OutcomeAE = "AE" // application error (NACK)
)

// nopMetrics is a no-op MetricsEmitter used as the default when callers
// do not supply one. Keeps Listener internals nil-safe without per-call
// guards.
type nopMetrics struct{}

func (nopMetrics) Inc(string, map[string]string)              {}
func (nopMetrics) Add(string, float64, map[string]string)     {}
func (nopMetrics) Observe(string, float64, map[string]string) {}
func (nopMetrics) Set(string, float64, map[string]string)     {}
