// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package meditechadapter_test

import (
	"context"
	"testing"
	"time"

	meditechadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/meditech"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

func TestMeditechManifestShape(t *testing.T) {
	t.Parallel()
	m := meditechadapter.New().Manifest()
	if m.ID != "meditech" {
		t.Errorf("Manifest.ID = %q, want meditech", m.ID)
	}
	if m.Vendor != "MEDITECH" {
		t.Errorf("Manifest.Vendor = %q", m.Vendor)
	}
	if !m.Capabilities.HL7Processor {
		t.Error("meditech adapter should declare HL7Processor capability")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("SpiVersion = %v", m.SpiVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
}

func TestMeditechRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	r := meditechadapter.NewRegistered()
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "meditech",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if a.Manifest().ID != "meditech" {
		t.Errorf("loaded id = %q", a.Manifest().ID)
	}
}

func TestMeditechHl7ProcessorBuildsAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	p := meditechadapter.New().BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor nil")
	}
	if _, err := p.Lex([]byte("MSH|^~\\&|MEDITECH|FAC|REC|FAC|202604261015||ADT^A01|1|P|2.5\r")); err != nil {
		t.Fatalf("Lex err = %v", err)
	}
}
