// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import "net/http"

// livenessHandler — stub. Real implementation lands in the GREEN commit.
type livenessHandler struct{}

func newLivenessHandler(reg *registry, metrics MetricsEmitter) *livenessHandler { return nil }

func (h *livenessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
