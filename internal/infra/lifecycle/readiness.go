// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"net/http"
	"time"
)

// readinessHandler — stub. Real implementation lands in the GREEN commit.
type readinessHandler struct{}

func newReadinessHandler(reg *registry, metrics MetricsEmitter, perCheckTimeout time.Duration) *readinessHandler {
	return nil
}

func (h *readinessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
