// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net/http"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/fhirerror"
)

func writeAuthFailure(w http.ResponseWriter, status int, reason string) {
	code := fhirerror.CodeLogin
	if status == http.StatusForbidden {
		code = fhirerror.CodeForbidden
	}
	fhirerror.WriteError(w, status, code, reason)
}
