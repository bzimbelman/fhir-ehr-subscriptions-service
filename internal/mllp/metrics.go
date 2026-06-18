// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

// MetricsEmitter is the metrics seam between the listener and the host's
// observability stack. The listener emits typed events; an adapter at the
// host translates them to Prometheus, OTLP, or whatever the deployment
// wires up. Tests use a fake.
//
// All counter / histogram / gauge names emitted from this package carry
// the canonical fhir_subs_mllp_ prefix already (ADR 0008 #10, LLD §7).
// Emitters render them on the wire as-is — no host-side rewrap.
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

// Metric names emitted by the listener. These are the canonical wire
// names (LLD §7); emitters render them as-is.
const (
	MetricMessagesReceivedTotal = "fhir_subs_mllp_received_total"
	MetricMessagesAckedTotal    = "fhir_subs_mllp_ack_total"
	MetricMessageBytes          = "fhir_subs_mllp_message_bytes"
	MetricMalformedTotal        = "fhir_subs_mllp_malformed_total"
	MetricNackTotal             = "fhir_subs_mllp_nack_total"
	MetricPersistDurationMS     = "fhir_subs_mllp_persist_duration_ms"
	MetricActiveConnections     = "fhir_subs_mllp_active_connections"
	MetricInflightPerConnection = "fhir_subs_mllp_inflight_per_connection"
	MetricAcceptErrorsTotal     = "fhir_subs_mllp_accept_errors"
	MetricReadErrorsTotal       = "fhir_subs_mllp_read_errors"
	MetricDisconnectMidFrame    = "fhir_subs_mllp_disconnect_mid_frame"
	MetricDropForPersistFails   = "fhir_subs_mllp_drop_for_persist_failures"

	// MetricConnectionsRefusedTotal counts TCP connections rejected by
	// the admission semaphore (B-19): MaxConnections or
	// MaxConnectionsPerIP exceeded.
	MetricConnectionsRefusedTotal = "fhir_subs_mllp_connections_refused_total"
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
