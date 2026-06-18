// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package allscriptsadapter_test

import (
	"context"
	"testing"
	"time"

	allscriptsadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/allscripts"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

func TestAllscriptsManifestShape(t *testing.T) {
	t.Parallel()
	m := allscriptsadapter.New().Manifest()
	if m.ID != "allscripts" {
		t.Errorf("Manifest.ID = %q, want allscripts", m.ID)
	}
	if m.Vendor != "Allscripts (Veradigm)" {
		t.Errorf("Manifest.Vendor = %q", m.Vendor)
	}
	if !m.Capabilities.HL7Processor {
		t.Error("allscripts adapter should declare HL7Processor capability")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("SpiVersion = %v", m.SpiVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

func TestAllscriptsRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	r := allscriptsadapter.NewRegistered()
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "allscripts",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if a.Manifest().ID != "allscripts" {
		t.Errorf("loaded id = %q", a.Manifest().ID)
	}
}

func TestAllscriptsHl7ProcessorBuildsAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	p := allscriptsadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor nil")
	}
	if _, err := p.Lex([]byte("MSH|^~\\&|ALLSCRIPTS|FAC|REC|FAC|202604261015||ADT^A01|1|P|2.5\r")); err != nil {
		t.Fatalf("Lex err = %v", err)
	}
}
