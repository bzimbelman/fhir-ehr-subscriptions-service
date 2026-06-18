// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package directadapter_test

import (
	"context"
	"testing"
	"time"

	directadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/direct"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

func TestDirectManifestShape(t *testing.T) {
	t.Parallel()
	m := directadapter.New().Manifest()
	if m.ID != "direct" {
		t.Errorf("Manifest.ID = %q, want direct", m.ID)
	}
	if m.Vendor != "Direct Project / DirectTrust" {
		t.Errorf("Manifest.Vendor = %q", m.Vendor)
	}
	if !m.Capabilities.HL7Processor {
		t.Error("direct adapter should declare HL7Processor capability (raw-bytes pipeline reuse)")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("SpiVersion = %v", m.SpiVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

func TestDirectRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	r := directadapter.NewRegistered()
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "direct",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if a.Manifest().ID != "direct" {
		t.Errorf("loaded id = %q", a.Manifest().ID)
	}
}

func TestDirectHl7ProcessorBuildsAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	p := directadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor nil")
	}
	// Direct messages are SMTP envelopes, not HL7 v2 — but the SPI surface is
	// shared and passthrough must succeed on raw bytes.
	if _, err := p.Lex([]byte("From: provider@direct.example.com\r\nTo: subscriber@direct.example.com\r\nSubject: CCD\r\n\r\n<ClinicalDocument/>")); err != nil {
		t.Fatalf("Lex err = %v", err)
	}
}
