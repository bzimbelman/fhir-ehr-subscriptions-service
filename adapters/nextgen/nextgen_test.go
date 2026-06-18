// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package nextgenadapter_test

import (
	"context"
	"testing"
	"time"

	nextgenadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/nextgen"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

func TestNextGenManifestShape(t *testing.T) {
	t.Parallel()
	m := nextgenadapter.New().Manifest()
	if m.ID != "nextgen" {
		t.Errorf("Manifest.ID = %q, want nextgen", m.ID)
	}
	if m.Vendor != "NextGen Healthcare" {
		t.Errorf("Manifest.Vendor = %q", m.Vendor)
	}
	if !m.Capabilities.HL7Processor {
		t.Error("nextgen adapter should declare HL7Processor capability")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("SpiVersion = %v", m.SpiVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

func TestNextGenRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	r := nextgenadapter.NewRegistered()
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "nextgen",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if a.Manifest().ID != "nextgen" {
		t.Errorf("loaded id = %q", a.Manifest().ID)
	}
}

func TestNextGenHl7ProcessorBuildsAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	p := nextgenadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor nil")
	}
	if _, err := p.Lex([]byte("MSH|^~\\&|NEXTGEN|FAC|REC|FAC|202604261015||ADT^A01|1|P|2.5\r")); err != nil {
		t.Fatalf("Lex err = %v", err)
	}
}
