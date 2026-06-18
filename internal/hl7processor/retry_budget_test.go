// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// S-9.9 unit-level coverage for the per-row retry budget on
// hl7processor BeginTx failures. The integration-level coverage that
// proves a real BeginTx failure dead-letters the row lives in
// retry_budget_integration_test.go (build tag: integration).

package hl7processor

import (
	"testing"
)

// TestS9_9_Config_MaxRowAttempts_DefaultIsPositive — the knob defaults
// to a positive value so a zero-init Config does not silently disable
// the retry budget.
func TestS9_9_Config_MaxRowAttempts_DefaultIsPositive(t *testing.T) {
	t.Parallel()
	c := Config{AdapterID: "default"}.withDefaults()
	if c.MaxRowAttempts <= 0 {
		t.Fatalf("MaxRowAttempts default must be positive, got %d", c.MaxRowAttempts)
	}
	if c.MaxRowAttempts != DefaultMaxRowAttempts {
		t.Errorf("MaxRowAttempts default = %d; want %d", c.MaxRowAttempts, DefaultMaxRowAttempts)
	}
}

// TestS9_9_Config_MaxRowAttempts_OverrideRespected — explicit non-zero
// values pass through withDefaults unchanged.
func TestS9_9_Config_MaxRowAttempts_OverrideRespected(t *testing.T) {
	t.Parallel()
	c := Config{AdapterID: "default", MaxRowAttempts: 3}.withDefaults()
	if c.MaxRowAttempts != 3 {
		t.Errorf("MaxRowAttempts override dropped: got %d, want 3", c.MaxRowAttempts)
	}
}

// TestS9_9_ErrorClassTxBeginFailed_RoutesToUnparseable — a row that
// dead-letters via the BeginTx-budget path lands as
// dead_letters.kind='hl7_unparseable' (no FHIR resource ever existed).
func TestS9_9_ErrorClassTxBeginFailed_RoutesToUnparseable(t *testing.T) {
	t.Parallel()
	if got := dlKindForClass(ErrorClassTxBeginFailed); got != "hl7_unparseable" {
		t.Errorf("dlKindForClass(tx_begin_failed) = %q; want hl7_unparseable", got)
	}
}

// TestS9_9_ErrorClassTxBeginFailed_StringForm — the wire form used in
// metric labels and structured logs is "tx_begin_failed".
func TestS9_9_ErrorClassTxBeginFailed_StringForm(t *testing.T) {
	t.Parallel()
	if ErrorClassTxBeginFailed.String() != "tx_begin_failed" {
		t.Errorf("string form: got %q want tx_begin_failed", ErrorClassTxBeginFailed.String())
	}
}

// TestS9_9_OutcomeTxBeginFailedLabel — the messages_processed label
// used for the BeginTx-failure path is wired and stable.
func TestS9_9_OutcomeTxBeginFailedLabel(t *testing.T) {
	t.Parallel()
	if OutcomeTxBeginFailed != "tx_begin_failed" {
		t.Errorf("OutcomeTxBeginFailed = %q; want tx_begin_failed", OutcomeTxBeginFailed)
	}
}
