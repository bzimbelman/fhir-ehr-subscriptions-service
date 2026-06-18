// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package demoadapter is an illustrative HL7-to-FHIR translator for demos.
// It is NOT Epic-grade and does not pretend to handle the full ORU^R01
// surface. The goal is to produce a recognizable FHIR Observation from a
// minimal ORU^R01 message so a downstream matcher sees `patient=<MRN>`,
// the OBX-3 LOINC code, and the OBX-5 value on the resulting resource.
//
// What it does:
//   - Lex parses the canonical ER7 segment grid (segment terminator \r,
//     field separator |, component separator ^).
//   - Classify accepts ORU^R01 only; other trigger events return an error.
//   - MapToFHIR builds an Observation with subject, code, value, and
//     effectiveDateTime from MSH-7, PID-3, OBR-7, and OBX-3/5/11.
//
// What it does not do: cancel/replace correlation, multi-OBX bundling, NTE
// notes, schema validation, or anything beyond a single OBX. Vendor adapters
// (epic, meditech, ...) implement the real surface.
package demoadapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// Adapter is the demo adapter.
type Adapter struct {
	spi.BaseEhrAdapter
}

// New constructs a fresh demo adapter instance.
func New() *Adapter { return &Adapter{} }

// NewRegistered returns a registry pre-populated with the demo adapter.
func NewRegistered() *registry.Registry {
	r := registry.New()
	if err := r.Register("demo", func() spi.EhrAdapter { return New() }); err != nil {
		panic(err)
	}
	return r
}

// Manifest returns the demo adapter's declared manifest. The demo adapter
// only contributes an HL7 processor; FHIR scan / vendor API / hydration are
// not implemented because the demo flow is HL7 -> Observation only.
func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "demo",
		Vendor:               "fhir-ehr-subscriptions-service",
		Description:          "Illustrative ORU^R01 to FHIR Observation translator. Not Epic-grade.",
		SupportedEhrVersions: spi.VersionSpec("*"),
		Capabilities: spi.Capabilities{
			HL7Processor: true,
		},
		ConfigSchema: []byte(`{"type":"object","additionalProperties":true}`),
		SpiVersion:   spi.HostSPIVersion,
	}
}

// BuildHl7Processor returns the demo HL7 processor.
func (a *Adapter) BuildHl7Processor(_ spi.AdapterContext) spi.Hl7MessageProcessor {
	return &hl7Processor{}
}

// BuildFhirScanRunner returns nil; the demo adapter has no scan plan.
func (a *Adapter) BuildFhirScanRunner(_ spi.AdapterContext) spi.FhirScanRunner { return nil }

// BuildVendorAPIClient returns nil; no vendor proprietary feed.
func (a *Adapter) BuildVendorAPIClient(_ spi.AdapterContext) spi.VendorAPIClient { return nil }

// BuildHydrationService returns nil; the demo adapter does not support
// full-resource notification hydration.
func (a *Adapter) BuildHydrationService(_ spi.AdapterContext) spi.HydrationService { return nil }

type hl7Processor struct {
	spi.BaseHl7MessageProcessor
}

// segments is the typed payload Lex stores in ParsedHL7Message.Segments.
// Each entry is one segment; fields[0] is the segment id (e.g., "MSH").
type segments struct {
	rows [][]string
}

// field returns the n-th 1-based field of the first segment matching id, or
// "" if missing. MSH is special: per HL7 the field separator IS field 1, so
// MSH-1 is "|" and MSH-2 is "^~\&"; we shift accordingly so callers can use
// the documented HL7 indices (MSH-7, MSH-9, MSH-10, ...).
func (s *segments) field(id string, idx int) string {
	for _, row := range s.rows {
		if len(row) == 0 || row[0] != id {
			continue
		}
		if id == "MSH" {
			// MSH-1 is the field separator; we synthesize it.
			if idx == 1 {
				return "|"
			}
			// MSH-2 is row[1] ("^~\&"), MSH-3 is row[2], etc.
			i := idx - 1
			if i < 0 || i >= len(row) {
				return ""
			}
			return row[i]
		}
		// Non-MSH: row[0] is the segment id; field n is row[n].
		if idx < 0 || idx >= len(row) {
			return ""
		}
		return row[idx]
	}
	return ""
}

func (h *hl7Processor) Lex(raw []byte) (spi.ParsedHL7Message, error) {
	cp := make([]byte, len(raw))
	copy(cp, raw)
	segs := &segments{}
	for _, line := range strings.Split(strings.TrimRight(string(cp), "\r"), "\r") {
		if line == "" {
			continue
		}
		segs.rows = append(segs.rows, strings.Split(line, "|"))
	}
	return spi.ParsedHL7Message{Raw: cp, Segments: segs}, nil
}

// errUnsupportedTrigger is returned by Classify for non-ORU^R01 messages.
var errUnsupportedTrigger = errors.New("demo adapter: only ORU^R01 is supported")

func (h *hl7Processor) Classify(parsed spi.ParsedHL7Message) (spi.Classification, error) {
	segs, ok := parsed.Segments.(*segments)
	if !ok || segs == nil {
		return spi.Classification{}, errors.New("demo adapter: parsed segments missing")
	}
	if got := segs.field("MSH", 9); !strings.HasPrefix(got, "ORU^R01") {
		return spi.Classification{}, fmt.Errorf("%w: MSH-9=%q", errUnsupportedTrigger, got)
	}
	return spi.Classification{
		Kind:           spi.ChangeCreate,
		CorrelationKey: segs.field("MSH", 10), // MSH-10 control id
	}, nil
}

func (h *hl7Processor) MapToFHIR(parsed spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
	segs, ok := parsed.Segments.(*segments)
	if !ok || segs == nil {
		return spi.FhirResource{}, errors.New("demo adapter: parsed segments missing")
	}

	mrn := firstComponent(segs.field("PID", 3))
	if mrn == "" {
		return spi.FhirResource{}, errors.New("demo adapter: PID-3 MRN missing")
	}
	ctrlID := segs.field("MSH", 10)

	// Effective time: prefer OBR-7 (observation date/time) if present, else
	// fall back to MSH-7 (message timestamp).
	tsRaw := segs.field("OBR", 7)
	if tsRaw == "" {
		tsRaw = segs.field("MSH", 7)
	}
	effectiveISO, err := hl7TSToRFC3339(tsRaw)
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("demo adapter: invalid effective timestamp %q: %w", tsRaw, err)
	}

	// OBX-3 carries the observation code as code^display^system. The demo
	// assumes LOINC for the test fixtures; if the third component is "LN"
	// or empty, we stamp http://loinc.org so the matcher sees a stable URL.
	codeField := segs.field("OBX", 3)
	codeParts := strings.Split(codeField, "^")
	codeVal, codeDisplay, codeSystem := "", "", ""
	if len(codeParts) > 0 {
		codeVal = codeParts[0]
	}
	if len(codeParts) > 1 {
		codeDisplay = codeParts[1]
	}
	if len(codeParts) > 2 {
		codeSystem = loincURL(codeParts[2])
	}
	if codeSystem == "" {
		codeSystem = "http://loinc.org"
	}

	obs := map[string]any{
		"resourceType":      "Observation",
		"id":                ctrlID,
		"status":            statusFromOBX11(segs.field("OBX", 11)),
		"subject":           map[string]string{"reference": "Patient/" + mrn},
		"effectiveDateTime": effectiveISO,
		"code": map[string]any{
			"coding": []map[string]string{
				{
					"system":  codeSystem,
					"code":    codeVal,
					"display": codeDisplay,
				},
			},
		},
	}

	// OBX-2 is the value type (NM = numeric, ST = string, ...). OBX-5 is
	// the value, OBX-6 is units.
	switch segs.field("OBX", 2) {
	case "NM":
		valStr := segs.field("OBX", 5)
		if v, perr := strconv.ParseFloat(valStr, 64); perr == nil {
			obs["valueQuantity"] = map[string]any{
				"value": v,
				"unit":  segs.field("OBX", 6),
			}
		} else {
			obs["valueString"] = valStr
		}
	default:
		if v := segs.field("OBX", 5); v != "" {
			obs["valueString"] = v
		}
	}

	body, err := json.Marshal(obs)
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("demo adapter: marshal Observation: %w", err)
	}
	return spi.FhirResource{
		ResourceType: "Observation",
		ID:           ctrlID,
		Body:         body,
	}, nil
}

// firstComponent returns the substring before the first '^', i.e. the first
// HL7 component of a composite field. PID-3 is `MRN^^^assigner^MR`; MRN is
// the first component.
func firstComponent(field string) string {
	if i := strings.Index(field, "^"); i >= 0 {
		return field[:i]
	}
	return field
}

// hl7TSToRFC3339 converts an HL7 v2 TS (YYYYMMDDHHMMSS, optionally truncated
// to YYYYMMDDHHMM or YYYYMMDD) into RFC 3339 with Z. Tolerates trailing
// fractional seconds and timezone offsets by trimming after the leading 14
// digits.
func hl7TSToRFC3339(ts string) (string, error) {
	digits := ts
	if i := strings.IndexAny(ts, ".+-"); i >= 0 {
		digits = ts[:i]
	}
	if len(digits) > 14 {
		digits = digits[:14]
	}
	for _, layout := range []string{"20060102150405", "200601021504", "20060102"} {
		if len(digits) == len(layout) {
			t, err := time.Parse(layout, digits)
			if err != nil {
				return "", err
			}
			return t.UTC().Format("2006-01-02T15:04:05Z"), nil
		}
	}
	return "", fmt.Errorf("unparseable HL7 TS %q", ts)
}

// loincURL maps the HL7 LOINC code-system marker to its canonical URL.
// Anything else is passed through verbatim — vendor adapters do real
// terminology resolution.
func loincURL(marker string) string {
	switch strings.ToUpper(marker) {
	case "", "LN", "L":
		return "http://loinc.org"
	default:
		return marker
	}
}

// statusFromOBX11 maps the HL7 v2 OBX-11 result status to a FHIR
// Observation.status code. Defaults to "final" because most demo paths use
// "F"; "C" (corrected) maps to "amended" and "P" (preliminary) to
// "preliminary".
func statusFromOBX11(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P":
		return "preliminary"
	case "C":
		return "amended"
	case "X":
		return "cancelled"
	default:
		return "final"
	}
}
