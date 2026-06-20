// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package main is the docs-lint tool. It walks operator-facing documentation
// (docs/, deploy/helm/) and asserts every CLI subcommand, metric name, port
// reference, and mkdocs navigation entry resolves to a real binary symbol.
//
// Pattern G of the H-series strategy: documentation lies invisible to code
// tests. This tool fails the build when docs and code disagree.
//
// Surfaces covered:
//
//   - mkdocs.yml nav links every docs/**/*.md (closes Finding #181).
//   - CLI subcommand references in docs/ resolve to a verb registered in
//     cmd/fhir-subs/main.go's dispatch.
//   - Metric names of the form fhir_subs_* cited in docs/ are registered in
//     internal/.
//   - Port references (`:NNNN` in fenced code or host:port shape) match a
//     port the binary opens (per deploy/helm/fhir-subs/values.yaml service.*).
//
// architecture.md YAML key checking and helm chart contract checking already
// live in cmd/fhir-subs (see docs_lint_test.go and helm_chart_test.go); this
// tool covers the surfaces those tests do not reach.
//
// Inline ignore sentinels are honored where a doc deliberately references a
// not-yet-wired symbol (deferred / future-work / known gap):
//
//	<!-- docs-lint:ignore-cli=dead-letters -->
//	<!-- docs-lint:ignore-metric=fhir_subs_matcher_topic_match_total -->
//	<!-- docs-lint:ignore-port=4318 -->
//	<!-- docs-lint:ignore-nav=presentation.md -->
//
// Each sentinel grants its scope (the rule + value) for the rest of the file
// it appears in. Removing an ignore is the natural moment to also remove the
// drift it concealed.
package main
