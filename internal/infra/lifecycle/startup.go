// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"net/http"
	"time"
)

// startupHandler — stub. Real implementation lands in the GREEN commit.
type startupHandler struct{}

func newStartupHandler(reg *registry, metrics MetricsEmitter, perCheckTimeout time.Duration) *startupHandler {
	return nil
}

func (h *startupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
