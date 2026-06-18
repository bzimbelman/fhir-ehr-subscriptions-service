// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestConfigValidate_RequiresAdapterID: empty AdapterID is rejected.
func TestConfigValidate_RequiresAdapterID(t *testing.T) {
	t.Parallel()
	cfg := Config{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty AdapterID")
	}
	cfg.AdapterID = "default"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

// TestConfigWithDefaults: zero values get filled in with package
// defaults; non-zero values pass through unchanged.
func TestConfigWithDefaults(t *testing.T) {
	t.Parallel()
	c := Config{AdapterID: "default"}.withDefaults()
	if c.ClaimBatchSize != DefaultClaimBatchSize {
		t.Errorf("ClaimBatchSize: %d", c.ClaimBatchSize)
	}
	if c.ClaimIdlePollInterval != DefaultClaimIdlePollInterval {
		t.Errorf("ClaimIdlePollInterval: %v", c.ClaimIdlePollInterval)
	}
	if c.ReaperTickInterval != DefaultReaperTickInterval {
		t.Errorf("ReaperTickInterval: %v", c.ReaperTickInterval)
	}

	// Pass-through of operator overrides.
	c2 := Config{
		AdapterID:             "epic",
		ClaimBatchSize:        4,
		ClaimIdlePollInterval: 250 * time.Millisecond,
		ReaperTickInterval:    1 * time.Second,
	}.withDefaults()
	if c2.ClaimBatchSize != 4 || c2.ClaimIdlePollInterval != 250*time.Millisecond || c2.ReaperTickInterval != time.Second {
		t.Errorf("unexpected: %+v", c2)
	}
}

// TestNew_RejectsMissingDeps: every required dependency is enforced.
func TestNew_RejectsMissingDeps(t *testing.T) {
	t.Parallel()
	cdc, err := codec.New(codec.NewStaticKeyProvider(map[int32][]byte{1: bytesOfLen(32)}, 1))
	if err != nil {
		t.Fatal(err)
	}

	full := Deps{
		Pool:       &pgxpool.Pool{},
		Codec:      cdc,
		HL7Queue:   repos.NewHl7MessageQueueRepo(cdc),
		Pending:    repos.NewPendingPairsRepo(cdc),
		Changes:    repos.NewResourceChangesRepo(cdc),
		DeadLetter: repos.NewDeadLettersRepo(cdc),
		Adapter:    &fakeProcessor{},
	}

	type tc struct {
		name string
		mut  func(d *Deps)
		want string
	}
	cases := []tc{
		{"pool", func(d *Deps) { d.Pool = nil }, "Pool"},
		{"codec", func(d *Deps) { d.Codec = nil }, "Codec"},
		{"queue", func(d *Deps) { d.HL7Queue = nil }, "HL7Queue"},
		{"pending", func(d *Deps) { d.Pending = nil }, "Pending"},
		{"changes", func(d *Deps) { d.Changes = nil }, "Changes"},
		{"deadletter", func(d *Deps) { d.DeadLetter = nil }, "DeadLetter"},
		{"adapter", func(d *Deps) { d.Adapter = nil }, "Adapter"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := full
			tc.mut(&d)
			_, err := New(Config{AdapterID: "x"}, d)
			if err == nil {
				t.Fatalf("expected error for missing %s", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name missing field %q", err, tc.want)
			}
		})
	}
}

// TestNew_FillsNilOptionals: nil Metrics/Logger/Now fall back to safe
// defaults so consumers don't need to set them.
func TestNew_FillsNilOptionals(t *testing.T) {
	t.Parallel()
	cdc, err := codec.New(codec.NewStaticKeyProvider(map[int32][]byte{1: bytesOfLen(32)}, 1))
	if err != nil {
		t.Fatal(err)
	}
	full := Deps{
		Pool:       &pgxpool.Pool{},
		Codec:      cdc,
		HL7Queue:   repos.NewHl7MessageQueueRepo(cdc),
		Pending:    repos.NewPendingPairsRepo(cdc),
		Changes:    repos.NewResourceChangesRepo(cdc),
		DeadLetter: repos.NewDeadLettersRepo(cdc),
		Adapter:    &fakeProcessor{},
	}
	p, err := New(Config{AdapterID: "default"}, full)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.metrics() == nil {
		t.Error("metrics() returned nil")
	}
	if p.deps.Logger == nil {
		t.Error("logger is nil")
	}
	if p.deps.Now == nil {
		t.Error("Now is nil")
	}
}

// TestPendingKindMapping: round-trip SPI <-> repo enum.
func TestPendingKindMapping(t *testing.T) {
	t.Parallel()
	if pendingKindFromChange(spi.ChangeCreate) != repos.PendingCreate {
		t.Error("create map")
	}
	if pendingKindFromChange(spi.ChangeDelete) != repos.PendingDelete {
		t.Error("delete map")
	}
	if pendingKindFromChange(spi.ChangeUpdate) != repos.PendingDelete {
		t.Error("update should default to delete (only delete/create are valid pending kinds)")
	}
	if pendingKindToChange(repos.PendingCreate) != spi.ChangeCreate {
		t.Error("create map back")
	}
	if pendingKindToChange(repos.PendingDelete) != spi.ChangeDelete {
		t.Error("delete map back")
	}
}

// TestDLKindForClass: validation -> hl7_invalid_fhir; everything else -> hl7_unparseable.
func TestDLKindForClass(t *testing.T) {
	t.Parallel()
	if dlKindForClass(ErrorClassValidation) != "hl7_invalid_fhir" {
		t.Error("validation")
	}
	for _, c := range []ErrorClass{ErrorClassParse, ErrorClassClassify, ErrorClassMap, ErrorClassUnexpected} {
		if dlKindForClass(c) != "hl7_unparseable" {
			t.Errorf("%s should map to hl7_unparseable", c)
		}
	}
}

// TestOutcomeLabel: each kind has a stable label.
func TestOutcomeLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		k    outcomeKind
		want string
	}{
		{outcomeEmitted, "emitted"},
		{outcomeHeld, "held"},
		{outcomeResolved, "resolved"},
		{outcomeDeadLetter, "dead_letter"},
	}
	for _, tc := range cases {
		if got := outcomeLabel(tc.k); got != tc.want {
			t.Errorf("kind %d: got %q want %q", tc.k, got, tc.want)
		}
	}
	if outcomeLabel(0) != "unknown" {
		t.Error("unknown fallback")
	}
}

func bytesOfLen(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}
