// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package cerneradapter_test

import (
	"context"
	"testing"
	"time"

	cerneradapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/cerner"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

func TestCernerManifestShape(t *testing.T) {
	t.Parallel()
	m := cerneradapter.New().Manifest()
	if m.ID != "cerner" {
		t.Errorf("Manifest.ID = %q, want cerner", m.ID)
	}
	if m.Vendor != "Oracle Health (Cerner)" {
		t.Errorf("Manifest.Vendor = %q", m.Vendor)
	}
	if !m.Capabilities.HL7Processor {
		t.Error("cerner adapter should declare HL7Processor capability")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("SpiVersion = %v, want %v", m.SpiVersion, spi.HostSPIVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

func TestCernerRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	r := cerneradapter.NewRegistered()
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "cerner",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if a.Manifest().ID != "cerner" {
		t.Errorf("loaded id = %q", a.Manifest().ID)
	}
}

func TestCernerHl7ProcessorBuildsAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	p := cerneradapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor nil")
	}
	if _, err := p.Lex([]byte("MSH|^~\\&|CERNER|FAC|REC|FAC|202604261015||ADT^A01|1|P|2.5\r")); err != nil {
		t.Fatalf("Lex err = %v", err)
	}
}
