// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package metrics is a placeholder for the observability metrics layer.
// This stub exists only to let TDD tests compile and FAIL on first run;
// real behavior lands in the green commit.
package metrics

import (
	"errors"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
)

// ErrInvalidName is returned when a metric name violates the prefix rule.
var ErrInvalidName = errors.New("metrics: invalid name")

// ErrInvalidLabel is returned when a label is not allowed on the requested metric type.
var ErrInvalidLabel = errors.New("metrics: invalid label")

// CounterOpts is a placeholder.
type CounterOpts struct {
	Name   string
	Help   string
	Labels []string
}

// HistogramOpts is a placeholder.
type HistogramOpts struct {
	Name    string
	Help    string
	Labels  []string
	Buckets []float64
}

// GaugeOpts is a placeholder.
type GaugeOpts struct {
	Name   string
	Help   string
	Labels []string
}

// Counter is a placeholder; implementation lands in the green commit.
type Counter struct{}

// Inc on a stub does nothing.
func (*Counter) Inc(prometheus.Labels) {}

// Add on a stub does nothing.
func (*Counter) Add(float64, prometheus.Labels) {}

// Histogram is a placeholder.
type Histogram struct{}

// Observe on a stub does nothing.
func (*Histogram) Observe(float64, prometheus.Labels) {}

// Gauge is a placeholder.
type Gauge struct{}

// Set on a stub does nothing.
func (*Gauge) Set(float64, prometheus.Labels) {}

// Inc on a stub does nothing.
func (*Gauge) Inc(prometheus.Labels) {}

// Dec on a stub does nothing.
func (*Gauge) Dec(prometheus.Labels) {}

// Emitter is a placeholder.
type Emitter struct{}

// New constructs an empty stub. Real impl in the green commit.
func New(_ *prometheus.Registry) *Emitter { return &Emitter{} }

// NewCounter is unimplemented in the failing-test stub.
func (*Emitter) NewCounter(CounterOpts) (*Counter, error) {
	return nil, errors.New("metrics: not yet implemented")
}

// NewHistogram is unimplemented in the failing-test stub.
func (*Emitter) NewHistogram(HistogramOpts) (*Histogram, error) {
	return nil, errors.New("metrics: not yet implemented")
}

// NewGauge is unimplemented in the failing-test stub.
func (*Emitter) NewGauge(GaugeOpts) (*Gauge, error) {
	return nil, errors.New("metrics: not yet implemented")
}

// Handler is unimplemented in the failing-test stub.
func (*Emitter) Handler() http.Handler { return nil }

// Inventory is the canonical startup metric set.
type Inventory struct {
	MetricsRegistered      *Gauge
	AuditWritesTotal       *Counter
	AuditChainInvalidTotal *Counter
	AuditSinkFailuresTotal *Counter
	LoggingPHIDroppedTotal *Counter
}

// RegisterStartupInventory is unimplemented in the failing-test stub.
func RegisterStartupInventory(*Emitter) (*Inventory, error) {
	return nil, errors.New("metrics: not yet implemented")
}
