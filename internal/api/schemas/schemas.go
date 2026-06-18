// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package schemas embeds the JSON schemas the Subscriptions API uses to
// validate incoming FHIR resources at request time. The schemas are
// project-defined and structural; profile-level checks (e.g., required
// extensions on the R4B Backport profile) are out of scope per ADR
// 0010 #8.
package schemas

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed subscription.schema.json
var subscriptionSchemaBytes []byte

var (
	subscriptionOnce   sync.Once
	subscriptionSchema *jsonschema.Schema
	subscriptionErr    error
)

func loadSubscriptionSchema() (*jsonschema.Schema, error) {
	subscriptionOnce.Do(func() {
		c := jsonschema.NewCompiler()
		if err := c.AddResource("subscription.schema.json",
			bytes.NewReader(subscriptionSchemaBytes)); err != nil {
			subscriptionErr = fmt.Errorf("schemas: load subscription: %w", err)
			return
		}
		schema, err := c.Compile("subscription.schema.json")
		if err != nil {
			subscriptionErr = fmt.Errorf("schemas: compile subscription: %w", err)
			return
		}
		subscriptionSchema = schema
	})
	return subscriptionSchema, subscriptionErr
}

// ValidateSubscription parses and validates body against the
// Subscription schema. Returns nil on success, a non-nil error whose
// Error() includes the offending field path on failure.
func ValidateSubscription(body []byte) error {
	schema, err := loadSubscriptionSchema()
	if err != nil {
		return err
	}
	var doc any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("schemas: subscription: invalid JSON: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return formatValidationErr("subscription", err)
	}
	return nil
}

// formatValidationErr turns the rich jsonschema error into a one-line
// summary suitable for OperationOutcome.diagnostics. Field paths are
// preserved for operator debugging.
func formatValidationErr(resource string, err error) error {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		paths := collectPaths(ve, nil)
		return fmt.Errorf("schemas: %s: %s", resource, strings.Join(paths, "; "))
	}
	return fmt.Errorf("schemas: %s: %w", resource, err)
}

func collectPaths(ve *jsonschema.ValidationError, acc []string) []string {
	if ve == nil {
		return acc
	}
	if len(ve.Causes) == 0 {
		path := ve.InstanceLocation
		if path == "" {
			path = "/"
		}
		// Map the instance path to a field name where helpful.
		acc = append(acc, fmt.Sprintf("%s: %s", strings.TrimPrefix(path, "/"), ve.Message))
		return acc
	}
	for _, c := range ve.Causes {
		acc = collectPaths(c, acc)
	}
	return acc
}
