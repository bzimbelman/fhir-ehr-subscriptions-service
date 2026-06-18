// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package metrics implements the Prometheus metrics layer of the
// observability module. Every metric registered through this package is
// validated against two project-wide rules:
//
//  1. Names must carry the fhir_subs_ prefix (ADR 0008 #10).
//  2. The label-cardinality table in the observability LLD §4.2 is
//     enforced at registration time:
//     - subscription_id is permitted only on gauges, never on counters
//     or histograms.
//     - peer_addr is permitted only on the MLLP listener's
//     _received_total counters; never on histograms; never on other
//     counters.
//
// The Emitter wraps a *prometheus.Registry. Re-registering the same
// metric (same name and same shape) returns the existing collector so
// startup metric registration is idempotent.
package metrics

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricPrefix is the canonical project metric prefix (ADR 0008 #10).
const MetricPrefix = "fhir_subs_"

// ErrInvalidName is returned when a metric name violates MetricPrefix.
var ErrInvalidName = errors.New("metrics: invalid name")

// ErrInvalidLabel is returned when a label is not allowed on the requested metric type.
var ErrInvalidLabel = errors.New("metrics: invalid label")

// CounterOpts is the typed registration shape for counters.
type CounterOpts struct {
	Name   string
	Help   string
	Labels []string
}

// HistogramOpts is the typed registration shape for histograms.
type HistogramOpts struct {
	Name    string
	Help    string
	Labels  []string
	Buckets []float64
}

// GaugeOpts is the typed registration shape for gauges.
type GaugeOpts struct {
	Name   string
	Help   string
	Labels []string
}

// Counter is a labeled counter handle.
type Counter struct {
	vec *prometheus.CounterVec
}

// Inc bumps the counter by 1 with the given labels (nil means no labels).
func (c *Counter) Inc(labels prometheus.Labels) {
	if labels == nil {
		c.vec.With(prometheus.Labels{}).Inc()
		return
	}
	c.vec.With(labels).Inc()
}

// Add bumps the counter by delta with the given labels.
func (c *Counter) Add(delta float64, labels prometheus.Labels) {
	if labels == nil {
		c.vec.With(prometheus.Labels{}).Add(delta)
		return
	}
	c.vec.With(labels).Add(delta)
}

// Histogram is a labeled histogram handle.
type Histogram struct {
	vec *prometheus.HistogramVec
}

// Observe records a histogram sample.
func (h *Histogram) Observe(value float64, labels prometheus.Labels) {
	if labels == nil {
		h.vec.With(prometheus.Labels{}).Observe(value)
		return
	}
	h.vec.With(labels).Observe(value)
}

// Gauge is a labeled gauge handle.
type Gauge struct {
	vec *prometheus.GaugeVec
}

// Set sets the gauge value.
func (g *Gauge) Set(value float64, labels prometheus.Labels) {
	if labels == nil {
		g.vec.With(prometheus.Labels{}).Set(value)
		return
	}
	g.vec.With(labels).Set(value)
}

// Inc bumps the gauge by 1.
func (g *Gauge) Inc(labels prometheus.Labels) {
	if labels == nil {
		g.vec.With(prometheus.Labels{}).Inc()
		return
	}
	g.vec.With(labels).Inc()
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec(labels prometheus.Labels) {
	if labels == nil {
		g.vec.With(prometheus.Labels{}).Dec()
		return
	}
	g.vec.With(labels).Dec()
}

// Emitter is the observability metrics layer's typed handle. Components
// receive an *Emitter and call NewCounter / NewHistogram / NewGauge.
type Emitter struct {
	reg *prometheus.Registry
	mu  sync.Mutex
	// known metric handles keyed by name. We hand back the existing
	// handle on duplicate registration with the same shape.
	counters   map[string]*counterEntry
	histograms map[string]*histogramEntry
	gauges     map[string]*gaugeEntry
}

type counterEntry struct {
	c      *Counter
	labels []string
}

type histogramEntry struct {
	h      *Histogram
	labels []string
}

type gaugeEntry struct {
	g      *Gauge
	labels []string
}

// New constructs an Emitter backed by reg. If reg is nil, a fresh
// registry is created (mostly for tests; callers wiring the module
// always pass an explicit registry).
func New(reg *prometheus.Registry) *Emitter {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	return &Emitter{
		reg:        reg,
		counters:   make(map[string]*counterEntry),
		histograms: make(map[string]*histogramEntry),
		gauges:     make(map[string]*gaugeEntry),
	}
}

// Registry returns the underlying Prometheus registry.
func (e *Emitter) Registry() *prometheus.Registry { return e.reg }

// Handler returns the HTTP handler that serves the Prometheus exposition
// endpoint. The endpoint is unauthenticated; access control is at the
// network layer (LLD §4).
func (e *Emitter) Handler() http.Handler {
	return promhttp.HandlerFor(e.reg, promhttp.HandlerOpts{})
}

// NewCounter registers (or returns the existing) counter.
func (e *Emitter) NewCounter(opts CounterOpts) (*Counter, error) {
	if err := validateName(opts.Name); err != nil {
		return nil, err
	}
	if err := validateLabels(metricKindCounter, opts.Name, opts.Labels); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.counters[opts.Name]; ok {
		if !labelsEqual(existing.labels, opts.Labels) {
			return nil, fmt.Errorf("%w: counter %q already registered with different labels", ErrInvalidName, opts.Name)
		}
		return existing.c, nil
	}
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: opts.Name,
		Help: opts.Help,
	}, opts.Labels)
	if err := e.reg.Register(vec); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if cv, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
				c := &Counter{vec: cv}
				e.counters[opts.Name] = &counterEntry{c: c, labels: append([]string(nil), opts.Labels...)}
				return c, nil
			}
		}
		return nil, fmt.Errorf("metrics: register counter %q: %w", opts.Name, err)
	}
	c := &Counter{vec: vec}
	e.counters[opts.Name] = &counterEntry{c: c, labels: append([]string(nil), opts.Labels...)}
	return c, nil
}

// NewHistogram registers (or returns the existing) histogram.
func (e *Emitter) NewHistogram(opts HistogramOpts) (*Histogram, error) {
	if err := validateName(opts.Name); err != nil {
		return nil, err
	}
	if err := validateLabels(metricKindHistogram, opts.Name, opts.Labels); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.histograms[opts.Name]; ok {
		if !labelsEqual(existing.labels, opts.Labels) {
			return nil, fmt.Errorf("%w: histogram %q already registered with different labels", ErrInvalidName, opts.Name)
		}
		return existing.h, nil
	}
	vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    opts.Name,
		Help:    opts.Help,
		Buckets: opts.Buckets,
	}, opts.Labels)
	if err := e.reg.Register(vec); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if hv, ok := are.ExistingCollector.(*prometheus.HistogramVec); ok {
				h := &Histogram{vec: hv}
				e.histograms[opts.Name] = &histogramEntry{h: h, labels: append([]string(nil), opts.Labels...)}
				return h, nil
			}
		}
		return nil, fmt.Errorf("metrics: register histogram %q: %w", opts.Name, err)
	}
	h := &Histogram{vec: vec}
	e.histograms[opts.Name] = &histogramEntry{h: h, labels: append([]string(nil), opts.Labels...)}
	return h, nil
}

// NewGauge registers (or returns the existing) gauge.
func (e *Emitter) NewGauge(opts GaugeOpts) (*Gauge, error) {
	if err := validateName(opts.Name); err != nil {
		return nil, err
	}
	if err := validateLabels(metricKindGauge, opts.Name, opts.Labels); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.gauges[opts.Name]; ok {
		if !labelsEqual(existing.labels, opts.Labels) {
			return nil, fmt.Errorf("%w: gauge %q already registered with different labels", ErrInvalidName, opts.Name)
		}
		return existing.g, nil
	}
	vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: opts.Name,
		Help: opts.Help,
	}, opts.Labels)
	if err := e.reg.Register(vec); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if gv, ok := are.ExistingCollector.(*prometheus.GaugeVec); ok {
				g := &Gauge{vec: gv}
				e.gauges[opts.Name] = &gaugeEntry{g: g, labels: append([]string(nil), opts.Labels...)}
				return g, nil
			}
		}
		return nil, fmt.Errorf("metrics: register gauge %q: %w", opts.Name, err)
	}
	g := &Gauge{vec: vec}
	e.gauges[opts.Name] = &gaugeEntry{g: g, labels: append([]string(nil), opts.Labels...)}
	return g, nil
}

type metricKind int

const (
	metricKindCounter metricKind = iota
	metricKindHistogram
	metricKindGauge
)

func validateName(name string) error {
	if !strings.HasPrefix(name, MetricPrefix) {
		return fmt.Errorf("%w: %q does not start with %q", ErrInvalidName, name, MetricPrefix)
	}
	return nil
}

// validateLabels enforces the LLD §4.2 cardinality table. The
// rules below extend the original `subscription_id` / `peer_addr`
// guards to also reject high-cardinality labels (`endpoint`,
// `topic_url`, `client_id`, `correlation_id`, `actor_id`) on
// histograms and on counters that aren't single-cardinality (S-2.20).
func validateLabels(kind metricKind, name string, labels []string) error {
	for _, l := range labels {
		switch l {
		case "subscription_id":
			if kind != metricKindGauge {
				return fmt.Errorf("%w: subscription_id is permitted only on gauges (metric %q)", ErrInvalidLabel, name)
			}
		case "peer_addr":
			if kind == metricKindHistogram {
				return fmt.Errorf("%w: peer_addr is forbidden on histograms (metric %q)", ErrInvalidLabel, name)
			}
			if kind == metricKindCounter && !strings.HasSuffix(name, "_received_total") {
				return fmt.Errorf("%w: peer_addr is permitted only on listener _received_total counters (metric %q)", ErrInvalidLabel, name)
			}
		case "endpoint", "topic_url", "client_id", "correlation_id", "actor_id":
			// These identifiers are unbounded across the lifetime of a
			// deployment (one URL per subscription, one client_id per
			// principal, one correlation_id per request, etc.). They
			// are forbidden as metric labels everywhere; emit them as
			// log/span attributes instead (S-2.20).
			return fmt.Errorf("%w: %s is forbidden as a metric label (metric %q); emit it as a log/span attribute", ErrInvalidLabel, l, name)
		}
	}
	return nil
}

func labelsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Inventory is the canonical startup metric set this module is responsible
// for. Other modules register their own metrics; these are the audit and
// logging metrics specifically owned by observability.
type Inventory struct {
	MetricsRegistered      *Gauge
	AuditWritesTotal       *Counter
	AuditChainInvalidTotal *Counter
	AuditSinkFailuresTotal *Counter
	LoggingPHIDroppedTotal *Counter
}

// RegisterStartupInventory creates and registers the canonical metric set.
func RegisterStartupInventory(em *Emitter) (*Inventory, error) {
	mr, err := em.NewGauge(GaugeOpts{
		Name: "fhir_subs_observability_metrics_registered",
		Help: "Number of metrics registered by the observability module at startup.",
	})
	if err != nil {
		return nil, err
	}
	aw, err := em.NewCounter(CounterOpts{
		Name:   "fhir_subs_audit_writes_total",
		Help:   "Audit-log durable writes.",
		Labels: []string{"outcome"},
	})
	if err != nil {
		return nil, err
	}
	aci, err := em.NewCounter(CounterOpts{
		Name: "fhir_subs_audit_chain_invalid_total",
		Help: "Audit-log hash-chain verification failures.",
	})
	if err != nil {
		return nil, err
	}
	asf, err := em.NewCounter(CounterOpts{
		Name:   "fhir_subs_audit_sink_failures_total",
		Help:   "Audit-log real-time sink emission failures.",
		Labels: []string{"sink"},
	})
	if err != nil {
		return nil, err
	}
	lpd, err := em.NewCounter(CounterOpts{
		Name:   "fhir_subs_logging_phi_dropped_total",
		Help:   "Operational-log fields dropped by the PHI redactor.",
		Labels: []string{"field"},
	})
	if err != nil {
		return nil, err
	}
	mr.Set(5, nil)
	// Pre-register a zero-valued time series for each label-set we know
	// about so the /metrics endpoint exposes them at scrape time even
	// before the first event lands. This makes alert rules targeting
	// "absent_over_time(...)" behave correctly during startup.
	aw.Add(0, prometheus.Labels{"outcome": "success"})
	aw.Add(0, prometheus.Labels{"outcome": "failure"})
	aci.Add(0, nil)
	asf.Add(0, prometheus.Labels{"sink": "stdout"})
	lpd.Add(0, prometheus.Labels{"field": "body"})

	return &Inventory{
		MetricsRegistered:      mr,
		AuditWritesTotal:       aw,
		AuditChainInvalidTotal: aci,
		AuditSinkFailuresTotal: asf,
		LoggingPHIDroppedTotal: lpd,
	}, nil
}
