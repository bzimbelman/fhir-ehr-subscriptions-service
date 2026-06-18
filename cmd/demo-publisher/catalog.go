// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"gopkg.in/yaml.v3"
)

// Catalog is a YAML-driven script of HL7 v2 messages the publisher emits.
//
// Each MessageEntry binds a template (oru-r01, adt-a01) to a fields map
// supplied by the operator. The publisher walks Messages in order, sleeps
// for Delay between sends, and prints a colored line per send + ACK.
type Catalog struct {
	Messages []MessageEntry `yaml:"messages"`
}

// MessageEntry is one row in the catalog.
type MessageEntry struct {
	Description string            `yaml:"description"`
	Delay       time.Duration     `yaml:"delay"`
	Template    string            `yaml:"template"`
	Fields      map[string]string `yaml:"fields"`
}

// supportedTemplates is the closed set of templates the demo publisher
// understands. Adding a template means: (1) add it here, (2) add a
// build<Template> case in builder.go, (3) document required fields.
var supportedTemplates = map[string]bool{
	"oru-r01": true,
	"adt-a01": true,
}

// loadCatalog parses a YAML catalog from r and validates each entry.
//
// Validation rules:
//   - At least one message is required.
//   - Each entry's template must be in supportedTemplates.
//   - Each entry's fields must include patient_id (required by all
//     supported templates so the bridge can match against it).
func loadCatalog(r io.Reader) (*Catalog, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	var c Catalog
	dec := yaml.NewDecoder(newReaderFromBytes(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	if len(c.Messages) == 0 {
		return nil, errors.New("catalog: messages must be non-empty")
	}
	for i, m := range c.Messages {
		if m.Template == "" {
			return nil, fmt.Errorf("messages[%d]: template is required", i)
		}
		if !supportedTemplates[m.Template] {
			return nil, fmt.Errorf("messages[%d]: unknown template %q (supported: oru-r01, adt-a01)", i, m.Template)
		}
		if _, ok := m.Fields["patient_id"]; !ok {
			return nil, fmt.Errorf("messages[%d]: field patient_id is required for template %q", i, m.Template)
		}
	}
	return &c, nil
}

// newReaderFromBytes wraps a byte slice in an io.Reader. Kept tiny so the
// loadCatalog implementation reads top-to-bottom without ceremony.
func newReaderFromBytes(b []byte) io.Reader {
	return &bytesReader{b: b}
}

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
