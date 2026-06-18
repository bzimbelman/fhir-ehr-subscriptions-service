// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package epicadapter_test

import (
	"context"
	"testing"
	"time"

	epicadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/epic"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// The epic adapter is a P3.2 SPI scaffold: it proves the plug-in surface is
// real by declaring an Epic-specific manifest and constructing a non-nil
// Hl7MessageProcessor whose vendor mapping is still a TODO. Real Z-segment +
// FHIR profile work lands in follow-up stories.

func TestEpicManifestShape(t *testing.T) {
	t.Parallel()
	a := epicadapter.New()
	m := a.Manifest()

	if m.ID != "epic" {
		t.Errorf("Manifest.ID = %q, want epic", m.ID)
	}
	if m.Vendor != "Epic Systems" {
		t.Errorf("Manifest.Vendor = %q, want %q", m.Vendor, "Epic Systems")
	}
	if !m.Capabilities.HL7Processor {
		t.Error("epic adapter should declare HL7Processor capability (Z-segment + Interconnect feeds rely on HL7 v2)")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("Manifest.SpiVersion = %v, want %v", m.SpiVersion, spi.HostSPIVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Manifest.Validate() = %v, want nil", err)
	}
}

func TestEpicRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	r := epicadapter.NewRegistered()
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "epic",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("registry.Load(epic) = %v, want nil", err)
	}
	if a == nil || a.Manifest().ID != "epic" {
		t.Fatalf("loaded adapter = %v, want id=epic", a)
	}
}

func TestEpicHl7ProcessorBuildsAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	a := epicadapter.New()
	p := a.BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor returned nil despite HL7Processor=true in manifest")
	}
	// Lex is wired (delegates to passthrough TODO) — must not panic.
	parsed, err := p.Lex([]byte("MSH|^~\\&|EPIC|MAYO|REC|FAC|202604261015||ORM^O01|1|P|2.5\r"))
	if err != nil {
		t.Fatalf("Lex returned err = %v, want nil", err)
	}
	if len(parsed.Raw) == 0 {
		t.Error("Lex produced empty Raw bytes")
	}
}
