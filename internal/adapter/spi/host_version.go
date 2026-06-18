// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package spi

// HostSPIVersion is the SPI version this host implements. The framework
// loader compares an adapter's manifest.spi_version against this constant
// at startup: major must match exactly; adapter minor must be <= host minor.
//
// Bumping rules:
//   - Additive change (new optional field, new method with default impl):
//     bump minor. Existing adapters keep working.
//   - Required change (new mandatory method, removed field, changed
//     signature): bump major. Adapters built against the prior major must
//     be rebuilt or the host refuses to start.
var HostSPIVersion = SemVer{Major: 1, Minor: 0, Patch: 0}
