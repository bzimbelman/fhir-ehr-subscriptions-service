// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
)

// buildEffective projects the post-resolution generic tree into the typed
// Effective struct one domain at a time. The generic tree is also kept on
// Effective so the redaction walker can serialize it for $status, error
// reports, and audit log.
//
// The conversion is best-effort: missing keys land as zero values. The
// validator already rejected before we got here, so this is a defense-in-depth
// path; we use a JSON round-trip to honor the json: tags on configtypes.
func buildEffective(tree map[string]interface{}, rmap *redaction.Map) Effective {
	e := Effective{
		Tree:      tree,
		Redaction: rmap,
	}
	mapToStruct(tree["deployment"], &e.Deployment)
	mapToStruct(tree["server"], &e.Server)
	mapToStruct(tree["lifecycle"], &e.Lifecycle)
	mapToStruct(tree["storage"], &e.Storage)
	mapToStruct(tree["auth"], &e.Auth)
	mapToStruct(tree["topics"], &e.Topics)
	mapToStruct(tree["mllp_listener"], &e.MLLPListener)
	mapToStruct(tree["adapter"], &e.Adapter)
	mapToStruct(tree["channels"], &e.Channels)
	mapToStruct(tree["delivery"], &e.Delivery)
	mapToStruct(tree["observability"], &e.Observability)
	return e
}

// mapToStruct decodes a generic sub-tree into a typed struct via JSON. The
// JSON round-trip honors the json: tags on configtypes and gracefully
// ignores fields the operator did not set.
func mapToStruct(in, out interface{}) {
	if in == nil {
		return
	}
	js, err := json.Marshal(in)
	if err != nil {
		return
	}
	_ = json.Unmarshal(js, out)
}
