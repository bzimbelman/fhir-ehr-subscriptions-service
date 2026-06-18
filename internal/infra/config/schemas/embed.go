// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package schemas

import _ "embed"

//go:embed core/deployment.json
var coreDeploymentJSON []byte

//go:embed core/server.json
var coreServerJSON []byte

//go:embed core/lifecycle.json
var coreLifecycleJSON []byte

//go:embed core/storage.json
var coreStorageJSON []byte

//go:embed core/auth.json
var coreAuthJSON []byte

//go:embed core/topics.json
var coreTopicsJSON []byte

//go:embed core/delivery.json
var coreDeliveryJSON []byte

//go:embed core/observability.json
var coreObservabilityJSON []byte

//go:embed core/mllp_listener.json
var coreMLLPListenerJSON []byte

//go:embed core/adapter.json
var coreAdapterJSON []byte

//go:embed core/channels.json
var coreChannelsJSON []byte
