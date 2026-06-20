// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The three operations runbooks under docs/operations/ have a long
// history of advertising metrics, config keys, SQL columns, and
// concurrency primitives that the production binary does not actually
// expose. Audit findings #109, #121, #122, #123 (sup #3) catalogued the
// drift; OpenProject stories #163, #164, #165 own the rewrite.
//
// These tests pin the rewrite: every non-prose claim a runbook makes is
// asserted against the current code so future drift fails CI rather
// than silently lying to operators.
//
// The lint is intentionally narrow: only claims that are mechanically
// verifiable (metric names, SQL kinds, advisory-lock callsites) are
// pinned. Prose stays the runbook author's responsibility.

const (
	otelRecipesPath     = "../../docs/operations/otel-exporter-recipes.md"
	deadLettersPath     = "../../docs/operations/dead-letters-runbook.md"
	horizontalScalePath = "../../docs/operations/horizontal-scale.md"
	metricsRegistryPath = "../../internal/infra/observability/metrics/metrics.go"
	apiMetricsPath      = "../../internal/api/metrics/metrics.go"
	migration0001Path   = "../../internal/infra/storage/migrate/migrations/0001_init.sql"
	partitionPath       = "../../internal/infra/storage/partition/partition.go"
	retentionPath       = "../../internal/infra/storage/retention/retention.go"
	migratePath         = "../../internal/infra/storage/migrate/migrate.go"
	auditPgStorePath    = "../../internal/infra/observability/audit/pgstore.go"
)

// TestRunbooks_MetricNamesExistInRegistry asserts every fhir_subs_*
// metric cited in the operations runbooks is registered in the
// production metrics inventory. Prior versions of the runbooks invented
// metric names (`fhir_subs_observability_tracing_export_failures_total`,
// `fhir_subs_observability_tracing_spans_dropped_total`,
// `fhir_subs_hl7processor_queue_depth_gauge`,
// `fhir_subs_scheduler_dispatch_duration_seconds`) that no `register`
// call ever creates; an operator following the runbook's troubleshooting
// flow would query a metric that never appears.
func TestRunbooks_MetricNamesExistInRegistry(t *testing.T) {
	registered, err := loadRegisteredMetricNames(metricsRegistryPath)
	if err != nil {
		t.Fatalf("load registered metrics: %v", err)
	}
	apiRegistered, err := loadRegisteredMetricNames(apiMetricsPath)
	if err != nil {
		t.Fatalf("load api metrics: %v", err)
	}
	for k := range apiRegistered {
		registered[k] = true
	}
	if len(registered) == 0 {
		t.Fatalf("no fhir_subs_* metrics found in %s", metricsRegistryPath)
	}

	for _, doc := range []string{otelRecipesPath, deadLettersPath, horizontalScalePath} {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		cited := citedMetricNames(string(body))
		var missing []string
		for _, name := range cited {
			if !registered[name] {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("%s cites metric names not registered in %s:\n  %s",
				doc, metricsRegistryPath, strings.Join(missing, "\n  "))
		}
	}
}

// TestDeadLettersRunbook_KindsMatchSchema asserts that every Kind value
// the dead-letters runbook documents in its `kind` table matches a
// value in the dead_letters CHECK constraint. The migration is the
// authoritative source: the database refuses any kind outside the
// constraint, so a runbook claim of an extra kind is a lie that breaks
// when the operator tries to filter on it.
func TestDeadLettersRunbook_KindsMatchSchema(t *testing.T) {
	allowed, err := loadDeadLettersKinds(migration0001Path)
	if err != nil {
		t.Fatalf("load dead_letters kinds: %v", err)
	}
	if len(allowed) == 0 {
		t.Fatalf("no dead_letters kinds parsed from %s", migration0001Path)
	}

	body, err := os.ReadFile(deadLettersPath)
	if err != nil {
		t.Fatalf("read %s: %v", deadLettersPath, err)
	}
	cited := citedDeadLetterKinds(string(body))
	if len(cited) == 0 {
		t.Fatalf("no dead_letter kinds cited in %s — table or markdown shape changed", deadLettersPath)
	}

	var unknown []string
	for _, k := range cited {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Errorf("%s cites dead_letters.kind values not in the schema CHECK constraint at %s:\n  %s\n"+
			"  allowed: %s",
			deadLettersPath, migration0001Path,
			strings.Join(unknown, "\n  "),
			strings.Join(sortedKeys(allowed), ", "))
	}
}

// TestDeadLettersRunbook_NoUnwiredCLI asserts the runbook does NOT
// promise a `fhir-subs dead-letters list|replay|forget` CLI that the
// production binary does not register. OP #166 settled the open
// question by committing to the SQL surface in v1; the runbook MUST
// stay aligned so an operator scanning headings doesn't try a
// command that 404s in main.go's subcommand dispatch.
//
// The check is narrow: a literal `fhir-subs dead-letters ` invocation
// (verb after the binary name) is the failure mode operators copy-paste.
// Discussions of why no such CLI exists are fine — the assertion only
// flags the `fhir-subs <verb>` shape that suggests "run this".
func TestDeadLettersRunbook_NoUnwiredCLI(t *testing.T) {
	body, err := os.ReadFile(deadLettersPath)
	if err != nil {
		t.Fatalf("read %s: %v", deadLettersPath, err)
	}
	// Match `fhir-subs dead-letters` (with optional surrounding
	// backticks) anywhere — a positive hit means the runbook is
	// telling operators to run a verb that main.go does not register.
	bad := regexp.MustCompile("`?fhir-subs\\s+dead-letters`?")
	if bad.MatchString(string(body)) {
		t.Errorf("%s still references a `fhir-subs dead-letters` CLI invocation. "+
			"Per OP #166 the runbook commits to SQL recipes; rephrase any remaining "+
			"mentions so they do not look like a runnable command.", deadLettersPath)
	}
}

// TestDeadLettersRunbook_MetricsEndpointPort asserts the runbook does
// NOT advertise port 9090 for /metrics. The production binary mounts
// /metrics on the chi router that serves the FHIR API at
// server.http.bind; there is no separate metrics listener on 9090.
// Telling operators to curl :9090 sends them to a closed port.
func TestDeadLettersRunbook_MetricsEndpointPort(t *testing.T) {
	body, err := os.ReadFile(deadLettersPath)
	if err != nil {
		t.Fatalf("read %s: %v", deadLettersPath, err)
	}
	if regexp.MustCompile(`:9090/metrics`).MatchString(string(body)) {
		t.Errorf("%s references :9090/metrics; /metrics is mounted on the FHIR API "+
			"listener (server.http.bind) — there is no separate :9090 listener "+
			"in cmd/fhir-subs/wiring.go (see r.Handle(\"/metrics\", ...))",
			deadLettersPath)
	}
}

// TestHorizontalScale_PartitionAdvisoryLockClaim asserts the
// horizontal-scale runbook does NOT claim partition maintenance uses
// pg_advisory_xact_lock unless the partition package actually calls it.
// internal/infra/storage/partition/partition.go uses no advisory lock;
// idempotency comes from CREATE TABLE IF NOT EXISTS plus a per-statement
// lock_timeout. The retention sweeper does take pg_advisory_lock; the
// audit log uses pg_advisory_xact_lock; the migration runner uses
// pg_advisory_lock. Lumping partition maintenance in with the others
// misleads operators who reason about lock contention.
func TestHorizontalScale_PartitionAdvisoryLockClaim(t *testing.T) {
	partitionBody, err := os.ReadFile(partitionPath)
	if err != nil {
		t.Fatalf("read %s: %v", partitionPath, err)
	}
	partitionUsesAdvisory := regexp.MustCompile(`pg_advisory(_xact)?_lock`).MatchString(string(partitionBody))

	docBody, err := os.ReadFile(horizontalScalePath)
	if err != nil {
		t.Fatalf("read %s: %v", horizontalScalePath, err)
	}
	// The doc may mention the partition rotator, but it must not
	// attribute pg_advisory_xact_lock specifically to partition
	// maintenance unless the package actually uses it.
	tableRow := regexp.MustCompile(`(?i)Partition maintenance.*pg_advisory_xact_lock`)
	if !partitionUsesAdvisory && tableRow.MatchString(string(docBody)) {
		t.Errorf("%s claims `Partition maintenance | pg_advisory_xact_lock per maintenance run` "+
			"but %s contains no pg_advisory(_xact)?_lock callsite. Either the doc is "+
			"fictional or the package needs to wire the lock.", horizontalScalePath, partitionPath)
	}
}

// TestRunbooks_RetentionConfigPath asserts the runbooks reference the
// real retention config path. The production binary parses retention
// windows under `storage.retention.*` (cmd/fhir-subs/config.go::
// StorageRetentionConfig), not `Pipeline.Retention.*`.
func TestRunbooks_RetentionConfigPath(t *testing.T) {
	for _, doc := range []string{deadLettersPath, horizontalScalePath} {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		// Pipeline.Retention.* is the OLD shape that did not match the
		// loaded YAML.
		bad := regexp.MustCompile(`\bPipeline\.Retention\.`)
		if bad.MatchString(string(body)) {
			t.Errorf("%s references Pipeline.Retention.*; the loaded config path is "+
				"storage.retention.* — see StorageRetentionConfig in cmd/fhir-subs/config.go",
				doc)
		}
	}
}

// TestOTelRunbook_TLSConfigKeysWired asserts every tracing.* config key
// cited in the OTel recipes runbook resolves through the production
// Config struct. This catches drift in the opposite direction from
// TestArchitectureDocConfigKeysMatchConfigStruct: a recipe that names
// tracing.tls.cert_file is only useful if the loader actually parses it
// into TracingTLSConfig.
func TestOTelRunbook_TLSConfigKeysWired(t *testing.T) {
	body, err := os.ReadFile(otelRecipesPath)
	if err != nil {
		t.Fatalf("read %s: %v", otelRecipesPath, err)
	}

	// Walk the documented configuration table; every row's first
	// column is a `tracing.*` config key. We only assert table-cited
	// keys (not free-text mentions in code blocks) so the test stays
	// stable as the YAML examples evolve.
	cited := citedTracingConfigKeys(string(body))
	if len(cited) == 0 {
		t.Fatalf("no tracing.* keys cited in %s — runbook table shape changed", otelRecipesPath)
	}

	// The set of keys that map through the production Config struct.
	// Sourced from cmd/fhir-subs/config.go::TracingConfig +
	// TracingTLSConfig. Keep this list in lock-step with that struct
	// when fields are added.
	known := map[string]bool{
		"tracing.otlp_endpoint":    true,
		"tracing.sample_rate":      true,
		"tracing.exporter_timeout": true,
		"tracing.insecure":         true,
		"tracing.tls.cert_file":    true,
		"tracing.tls.key_file":     true,
		"tracing.tls.ca_file":      true,
		// tracing.headers.<name> is a free-form map; we accept any
		// header key, validated by the prefix below.
	}
	headerPrefix := "tracing.headers."

	var unknown []string
	for _, k := range cited {
		if known[k] {
			continue
		}
		if strings.HasPrefix(k, headerPrefix) {
			continue
		}
		unknown = append(unknown, k)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Errorf("%s documents tracing.* keys not present on TracingConfig "+
			"(see cmd/fhir-subs/config.go):\n  %s",
			otelRecipesPath, strings.Join(unknown, "\n  "))
	}
}

// loadRegisteredMetricNames extracts every Name: "fhir_subs_..."
// literal from the metrics inventory. We deliberately limit the scan
// to metrics.go (the registration site) rather than the whole tree;
// test fixtures and helpers register one-off metrics that are not
// part of the production surface.
func loadRegisteredMetricNames(path string) (map[string]bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`Name:\s*"(fhir_subs_[a-zA-Z0-9_]+)"`)
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(body), -1) {
		out[m[1]] = true
	}
	return out, nil
}

// citedMetricNames pulls every fhir_subs_* token from a markdown body.
// Matches inside backticks, inside fenced code blocks, and in prose;
// stops at the first non-identifier rune so trailing punctuation is
// dropped.
func citedMetricNames(md string) []string {
	re := regexp.MustCompile(`fhir_subs_[a-zA-Z0-9_]+`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllString(md, -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// loadDeadLettersKinds parses the CHECK (kind in (...)) clause from the
// initial migration. Returns the set of allowed kind strings.
func loadDeadLettersKinds(path string) (map[string]bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Anchor on the `kind text not null check (kind in (...))` line.
	re := regexp.MustCompile(`(?s)kind\s+text\s+not\s+null\s+check\s*\(\s*kind\s+in\s*\(([^)]*)\)\s*\)`)
	m := re.FindStringSubmatch(string(body))
	if m == nil {
		return nil, nil
	}
	out := map[string]bool{}
	for _, lit := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1) {
		out[lit[1]] = true
	}
	return out, nil
}

// citedDeadLetterKinds extracts kind values from the runbook's "What
// gets dead-lettered" table. Each row's first column is a backticked
// kind string.
func citedDeadLetterKinds(md string) []string {
	// Find the section between the table header and the next section
	// header; restrict the scan so we don't pick up any backticked
	// `delivery_exhausted` mention in prose.
	startRE := regexp.MustCompile(`(?m)^##\s+What gets dead-lettered`)
	endRE := regexp.MustCompile(`(?m)^##\s+`)

	startIdx := startRE.FindStringIndex(md)
	if startIdx == nil {
		return nil
	}
	rest := md[startIdx[1]:]
	endIdx := endRE.FindStringIndex(rest)
	if endIdx != nil {
		rest = rest[:endIdx[0]]
	}

	rowRE := regexp.MustCompile("^\\| `([a-z_]+)`")
	// Skip the literal "kind" header column placeholder some authors
	// surround in backticks; it is a column name, not a row value.
	skip := map[string]bool{"kind": true}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(rest, "\n") {
		m := rowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if skip[m[1]] || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// citedTracingConfigKeys extracts tracing.* keys from the OTel runbook's
// "Configuration surface" table. Each row begins with `| `tracing.X` |`.
func citedTracingConfigKeys(md string) []string {
	rowRE := regexp.MustCompile("^\\| `(tracing\\.[a-zA-Z0-9_.<>]+)`")
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(md, "\n") {
		m := rowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		// Normalize tracing.headers.<name> placeholder to
		// tracing.headers. so the test can match the prefix.
		key = strings.ReplaceAll(key, ".<name>", ".")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Sanity guard: the audit-log advisory-lock claim in horizontal-scale
// is verifiable; this test pins it so a future audit refactor that
// drops the lock surfaces in CI rather than silently breaking the
// runbook's claim. The retention + migration advisory-lock claims are
// also pinned — these are the three locks the runbook may attribute.
func TestHorizontalScale_AdvisoryLockClaimsAreVerifiable(t *testing.T) {
	tests := []struct {
		path  string
		match *regexp.Regexp
		why   string
	}{
		{retentionPath, regexp.MustCompile(`pg_advisory_lock`),
			"retention sweeper takes pg_advisory_lock per sweep"},
		{migratePath, regexp.MustCompile(`pg_advisory_lock`),
			"migration runner takes pg_advisory_lock for the apply window"},
		{auditPgStorePath, regexp.MustCompile(`pg_advisory_xact_lock`),
			"audit chain takes pg_advisory_xact_lock per append"},
	}
	for _, tc := range tests {
		body, err := os.ReadFile(tc.path)
		if err != nil {
			t.Errorf("read %s: %v", tc.path, err)
			continue
		}
		if !tc.match.MatchString(string(body)) {
			t.Errorf("%s no longer contains %s — %s. The horizontal-scale "+
				"runbook attributes this lock; either restore the call or "+
				"update the runbook.", tc.path, tc.match, tc.why)
		}
	}
}
