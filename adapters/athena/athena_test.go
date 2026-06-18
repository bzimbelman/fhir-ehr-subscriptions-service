// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package athenaadapter_test

import (
	"context"
	"testing"
	"time"

	athenaadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/athena"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

func TestAthenaManifestShape(t *testing.T) {
	t.Parallel()
	m := athenaadapter.New().Manifest()
	if m.ID != "athena" {
		t.Errorf("Manifest.ID = %q, want athena", m.ID)
	}
	if m.Vendor != "athenahealth" {
		t.Errorf("Manifest.Vendor = %q", m.Vendor)
	}
	if !m.Capabilities.HL7Processor {
		t.Error("athena adapter should declare HL7Processor capability")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("SpiVersion = %v", m.SpiVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

func TestAthenaRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	r := athenaadapter.NewRegistered()
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "athena",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if a.Manifest().ID != "athena" {
		t.Errorf("loaded id = %q", a.Manifest().ID)
	}
}

func TestAthenaHl7ProcessorBuildsAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	p := athenaadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor nil")
	}
	if _, err := p.Lex([]byte("MSH|^~\\&|ATHENA|FAC|REC|FAC|202604261015||ADT^A01|1|P|2.5\r")); err != nil {
		t.Fatalf("Lex err = %v", err)
	}
}
