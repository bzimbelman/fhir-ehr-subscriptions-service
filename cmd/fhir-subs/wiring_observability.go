// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log/slog"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// buildObservabilityConfig translates the operator-facing tracing /
// metrics / audit blocks into the observability package's typed
// shape. The mapping is 1:1 today; the helper exists so future
// non-trivial transforms (e.g. env-var interpolation, default
// elision) have a single owner (story #94 AC #1, #7).
func buildObservabilityConfig(cfg *Config) observability.Config {
	if cfg == nil {
		return observability.Config{}
	}
	return observability.Config{
		Metrics: observability.MetricsConfig{
			Bind: cfg.Metrics.Bind,
			Path: cfg.Metrics.Path,
		},
		Tracing: observability.TracingConfig{
			OTLPEndpoint:    cfg.Tracing.OTLPEndpoint,
			SampleRate:      cfg.Tracing.SampleRate,
			ExporterTimeout: cfg.Tracing.ExporterTimeout,
			Insecure:        cfg.Tracing.Insecure,
			TLSCertFile:     cfg.Tracing.TLS.CertFile,
			TLSKeyFile:      cfg.Tracing.TLS.KeyFile,
			TLSCAFile:       cfg.Tracing.TLS.CAFile,
			Headers:         cfg.Tracing.Headers,
		},
		Logging: observability.LoggingConfig{
			Level:  cfg.Deployment.LogLevel,
			Format: cfg.Deployment.LogFormat,
		},
		Audit: observability.AuditConfig{
			Sink:              cfg.Audit.Sink,
			FilePath:          cfg.Audit.FilePath,
			FileSyncMode:      cfg.Audit.FileSyncMode,
			FileBatchInterval: cfg.Audit.FileBatchInterval,
		},
	}
}

// logCatalogDiagnostics emits a single startup/reload line summarizing
// what the catalog now contains and one line per rejected/overridden
// candidate so operators see exactly which topic JSON file failed.
func logCatalogDiagnostics(logger *slog.Logger, dir string, report catalog.Report) {
	if report.Catalog == nil {
		logger.Warn("topic catalog: nil after Load (treating as empty)")
		return
	}
	logger.Info("topic catalog loaded",
		"dir", dir,
		"topics", len(report.Catalog.All()),
		"rejected", len(report.Rejected),
		"overridden", len(report.Overridden),
	)
	for _, rej := range report.Rejected {
		logger.Warn("topic rejected",
			"origin", rej.Origin,
			"url", rej.URL,
			"reason", rej.Reason,
		)
	}
	for _, ov := range report.Overridden {
		logger.Warn("topic override fallback", "fields", ov.LogFields())
	}
}
