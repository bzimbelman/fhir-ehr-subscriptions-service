// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package versionshim translates between the internal R5 model and R4B
// Backport on the wire. LLD §6 + §11.1 mandate a wire-version
// negotiation shim so clients pinned to either FHIR version interoperate
// with a single internal model.
package versionshim

import (
	"errors"
	"strings"
)

// Version is a supported FHIR wire version.
type Version string

// Supported FHIR versions.
const (
	R4B Version = "4.0"
	R5  Version = "5.0"
)

// String returns the version as the dotted form used in the
// `fhirVersion` content-type parameter (e.g., "4.0", "5.0").
func (v Version) String() string { return string(v) }

// ErrUnsupportedVersion is returned by Negotiate when the Accept header
// pins a FHIR version this server does not implement. Callers map this
// to HTTP 415 Unsupported Media Type.
var ErrUnsupportedVersion = errors.New("versionshim: unsupported FHIR version")

// Negotiate inspects the Accept header and returns the FHIR wire
// version the response should carry. An empty header, a wildcard
// (`*/*`), or `application/fhir+json` without a `fhirVersion` parameter
// all default to R5 — the server's native model.
//
// A media type with `fhirVersion=4.0` returns R4B; `fhirVersion=5.0`
// returns R5. Any other version returns ErrUnsupportedVersion.
func Negotiate(acceptHeader string) (Version, error) {
	header := strings.TrimSpace(acceptHeader)
	if header == "" || header == "*/*" {
		return R5, nil
	}

	for _, entry := range strings.Split(header, ",") {
		v, err := negotiateOne(strings.TrimSpace(entry))
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
	}

	return R5, nil
}

func negotiateOne(entry string) (Version, error) {
	if entry == "" || entry == "*/*" {
		return R5, nil
	}

	parts := strings.Split(entry, ";")
	mediaType := strings.TrimSpace(parts[0])
	if mediaType != "application/fhir+json" && mediaType != "application/json" && mediaType != "*/*" {
		// Unknown media type isn't this shim's concern — let other
		// negotiation pick it up.
		return "", nil
	}

	for _, p := range parts[1:] {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		if !strings.EqualFold(key, "fhirVersion") {
			continue
		}
		switch val {
		case "4.0", "4.0.1", "4.3", "4.3.0":
			return R4B, nil
		case "5.0", "5.0.0":
			return R5, nil
		default:
			return "", ErrUnsupportedVersion
		}
	}

	return R5, nil
}
