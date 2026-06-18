// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package versionshim translates between the internal R5 model and R4B
// Backport on the wire. LLD §6 + §11.1 mandate a wire-version
// negotiation shim so clients pinned to either FHIR version interoperate
// with a single internal model.
package versionshim

import (
	"encoding/json"
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

// RenderSubscriptionR4B converts the R5-native Subscription wire form
// into the R4B Backport IG shape (P2.4). The conversion is lossy in
// the sense that R5 fields with no R4B equivalent are dropped or
// projected onto the closest Backport extension; round-tripping is
// not a goal of this MVP.
//
// Inputs and outputs are both `[]byte` of JSON. The function works
// on a parsed map so it tolerates extra fields the renderer does not
// produce today (forward-compat with new top-level fields).
//
// MVP scope: only the Subscription resource is converted on the read
// path. SubscriptionTopic and the subscription-notification Bundle
// shape differences are tracked as a follow-up — those resources
// continue to emit the R5 shape regardless of negotiation. This
// matches the spec's expectation that the most active conformance
// surface is the Subscription resource.
//
// Mapping per the FHIR R5 Backport IG (R4B form) at hl7.org/fhir/uv/subscriptions-backport:
//
//   - resourceType:                stays "Subscription"
//   - status:                      stays as-is
//   - topic:               →       criteria   (the topic canonical URL becomes the R4B criteria string)
//   - channelType.code:    →       channel.type
//   - endpoint:            →       channel.endpoint
//   - content:             →       channel.payload (R4B uses the same field for content style)
//   - header:              →       channel.header (array of strings — R5 uses an array of objects; we
//     flatten {name,value} pairs into "Name: Value" strings per the IG)
//   - heartbeatPeriod:     →       channel.heartbeatPeriod (seconds, integer)
//   - timeout:             →       channel.timeout (seconds, integer)
//   - reason:                      stays at top-level
//   - end:                         stays at top-level
//   - error:                       stays at top-level
//   - id:                          stays at top-level
//   - filterBy:            →       criteria-filter Backport extension (we emit it under
//     `_criteria.extension` per the IG; if no filterBy is present,
//     the extension is omitted)
//
// On any unexpected shape we fall back to returning the input bytes
// unchanged — better to ship the R5 form than fail the read. The
// caller logs at the call site so operators see the diagnostic.
func RenderSubscriptionR4B(r5Body []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(r5Body, &doc); err != nil {
		return nil, errors.New("versionshim: invalid R5 Subscription JSON")
	}
	if rt, _ := doc["resourceType"].(string); rt != "Subscription" {
		// Not a Subscription — nothing to convert. Pass through.
		return r5Body, nil
	}

	out := map[string]any{
		"resourceType": "Subscription",
	}
	if v, ok := doc["id"]; ok {
		out["id"] = v
	}
	if v, ok := doc["status"]; ok {
		out["status"] = v
	}
	if v, ok := doc["reason"]; ok {
		out["reason"] = v
	}
	if v, ok := doc["end"]; ok {
		out["end"] = v
	}
	if v, ok := doc["error"]; ok {
		out["error"] = v
	}

	// topic → criteria.
	if v, ok := doc["topic"].(string); ok && v != "" {
		out["criteria"] = v
	}

	// channel sub-object.
	channel := map[string]any{}
	if ct, ok := doc["channelType"].(map[string]any); ok {
		if code, _ := ct["code"].(string); code != "" {
			channel["type"] = code
		}
	}
	if ep, ok := doc["endpoint"].(string); ok && ep != "" {
		channel["endpoint"] = ep
	}
	if c, ok := doc["content"].(string); ok && c != "" {
		channel["payload"] = c
	}
	if hp, ok := doc["heartbeatPeriod"]; ok {
		channel["heartbeatPeriod"] = hp
	}
	if to, ok := doc["timeout"]; ok {
		channel["timeout"] = to
	}
	if hdrs, ok := doc["header"]; ok {
		channel["header"] = flattenR4BHeaders(hdrs)
	}
	if len(channel) > 0 {
		out["channel"] = channel
	}

	// filterBy → _criteria.extension (Backport criteria-filter).
	if fbAny, ok := doc["filterBy"]; ok {
		if fbList, ok := fbAny.([]any); ok && len(fbList) > 0 {
			ext := make([]any, 0, len(fbList))
			for _, item := range fbList {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				ext = append(ext, map[string]any{
					"url":         "http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/backport-filter-criteria",
					"valueString": filterByToCriteriaString(m),
				})
			}
			if len(ext) > 0 {
				out["_criteria"] = map[string]any{"extension": ext}
			}
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return r5Body, nil
	}
	return b, nil
}

// flattenR4BHeaders converts R5 `header` (array of {name,value}
// objects) into the R4B Backport form (array of "Name: Value"
// strings). If the input is already an array of strings, pass it
// through.
func flattenR4BHeaders(h any) []any {
	arr, ok := h.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		switch v := item.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			name, _ := v["name"].(string)
			val, _ := v["value"].(string)
			if name != "" {
				out = append(out, name+": "+val)
			}
		}
	}
	return out
}

// filterByToCriteriaString renders one R5 filterBy clause as the
// Backport criteria-filter valueString (e.g.,
// `Patient/123` for {filterParameter:"patient",value:"Patient/123"}).
// The IG defines this as a free-form FHIR-search-style query string;
// we emit `<param>[:<modifier>][<comparator>]=<value>` in the order
// the schema declares.
func filterByToCriteriaString(c map[string]any) string {
	param, _ := c["filterParameter"].(string)
	mod, _ := c["modifier"].(string)
	cmp, _ := c["comparator"].(string)
	val, _ := c["value"].(string)
	out := param
	if mod != "" {
		out += ":" + mod
	}
	if cmp != "" {
		out += cmp
	}
	out += "=" + val
	return out
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
