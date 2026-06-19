// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	allscriptsadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/allscripts"
	athenaadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/athena"
	cerneradapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/cerner"
	defaultadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/default"
	demoadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/demo"
	directadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/direct"
	epicadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/epic"
	meditechadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/meditech"
	nextgenadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/nextgen"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	adapterspi "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// registerAllAdapters wires every bundled vendor adapter into the host
// registry. Story #113 (epic #91) — before this helper landed,
// buildProductionRuntime registered only "default" and any operator
// config of `adapter.id: cerner|epic|athena|nextgen|meditech|allscripts|
// direct|demo` failed at startup with UnknownAdapterError despite the
// adapter package being compiled into the image.
//
// The registration is unconditional: which adapter actually drives the
// host is decided at Load time by cfg.Adapter.ID. Adding a new vendor
// adapter means adding the import + one Register call here.
func registerAllAdapters(adReg *registry.Registry) error {
	type entry struct {
		id      string
		factory registry.Factory
	}

	entries := []entry{
		{"allscripts", func() adapterspi.EhrAdapter { return allscriptsadapter.New() }},
		{"athena", func() adapterspi.EhrAdapter { return athenaadapter.New() }},
		{"cerner", func() adapterspi.EhrAdapter { return cerneradapter.New() }},
		{"default", func() adapterspi.EhrAdapter { return defaultadapter.New() }},
		{"demo", func() adapterspi.EhrAdapter { return demoadapter.New() }},
		{"direct", func() adapterspi.EhrAdapter { return directadapter.New() }},
		{"epic", func() adapterspi.EhrAdapter { return epicadapter.New() }},
		{"meditech", func() adapterspi.EhrAdapter { return meditechadapter.New() }},
		{"nextgen", func() adapterspi.EhrAdapter { return nextgenadapter.New() }},
	}

	for _, e := range entries {
		if err := adReg.Register(e.id, e.factory); err != nil {
			return fmt.Errorf("register %q: %w", e.id, err)
		}
	}
	return nil
}
