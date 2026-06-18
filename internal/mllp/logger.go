// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

// Logger is the structured-log seam between the listener and the host's
// logging stack. The listener emits typed events; an adapter at the host
// renders them to slog/zap/zerolog/whatever the deployment uses.
//
// The fields map carries the LLD §7 stable subset: listener_endpoint,
// peer_addr, connection_id, correlation_id, mllp_message_id, event.
type Logger interface {
	Info(event string, fields map[string]any)
	Warn(event string, fields map[string]any)
	Error(event string, fields map[string]any)
}

// nopLogger is the default Logger when callers do not supply one.
type nopLogger struct{}

func (nopLogger) Info(string, map[string]any)  {}
func (nopLogger) Warn(string, map[string]any)  {}
func (nopLogger) Error(string, map[string]any) {}
