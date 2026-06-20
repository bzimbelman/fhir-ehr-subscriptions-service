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

// TestDirectManifestShape covers OP #175: the Direct adapter must NOT
// declare HL7Processor capability. Direct messaging is SMTP+S/MIME with
// XDM/CDA payloads, not HL7 v2 over MLLP — declaring HL7Processor=true
// is a capability lie because the framework's HL7 pipeline cannot accept
// SMTP body bytes. Until SMTP/S-MIME ingress is wired (separate story),
// the adapter declares NO capabilities; the manifest stays loadable so
// operators see Direct in the registry but no subsystem mis-routes
// SMTP traffic into the MLLP pipeline.
func TestDirectManifestShape(t *testing.T) {
	t.Parallel()
	m := directadapter.New().Manifest()
	if m.ID != "direct" {
		t.Errorf("Manifest.ID = %q, want direct", m.ID)
	}
	if m.Vendor != "Direct Project / DirectTrust" {
		t.Errorf("Manifest.Vendor = %q", m.Vendor)
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("SpiVersion = %v", m.SpiVersion)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	if m.Capabilities.HL7Processor {
		t.Error("OP #175: Direct must not declare HL7Processor capability — Direct is SMTP/S-MIME, not HL7 v2 over MLLP")
	}
	if m.Capabilities.FhirScanRunner {
		t.Error("Direct must not declare FhirScanRunner capability")
	}
	if m.Capabilities.VendorAPIClient {
		t.Error("Direct must not declare VendorAPIClient capability")
	}
	if m.Capabilities.HydrationService {
		t.Error("Direct must not declare HydrationService capability")
	}
}

// TestDirectRegistryRoundTrip covers OP #175: even with no capabilities,
// the Direct manifest must still register and load — operators need
// visibility that the adapter is bundled.
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

// TestDirectBuildersHonorCapabilities covers OP #175: with all
// capabilities false, every Build* method must return nil. The previous
// implementation returned a passthrough Hl7MessageProcessor that lex'd
// SMTP envelope bytes and emitted a hardcoded Bundle — a silent fake.
// Now BuildHl7Processor must return nil so the registry's
// validateCapabilities check stays consistent (no capability => no
// builder), and so the framework cannot accidentally route SMTP bytes
// through the MLLP pipeline.
func TestDirectBuildersHonorCapabilities(t *testing.T) {
	t.Parallel()
	a := directadapter.New()
	ctx := spi.AdapterContext{Now: time.Now}
	if a.BuildHl7Processor(ctx) != nil {
		t.Error("OP #175: BuildHl7Processor must return nil when HL7Processor capability is false")
	}
	if a.BuildFhirScanRunner(ctx) != nil {
		t.Error("BuildFhirScanRunner must return nil when capability is false")
	}
	if a.BuildVendorAPIClient(ctx) != nil {
		t.Error("BuildVendorAPIClient must return nil when capability is false")
	}
	if a.BuildHydrationService(ctx) != nil {
		t.Error("BuildHydrationService must return nil when capability is false")
	}
}
