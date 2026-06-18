// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package registry is the bundled-adapter registry the host uses to select
// one EhrAdapter at startup from the set compiled into the image. The shape
// is deliberately simple: a Register call wires (id -> factory), and Load
// (driven by validated config) returns one constructed adapter after running
// the LLD section 4 validations.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// Factory constructs a fresh EhrAdapter instance. The registry calls the
// factory exactly once per Load. Factories should not maintain global state.
type Factory func() spi.EhrAdapter

// Registry is the (id -> Factory) map the host loads adapters from.
type Registry struct {
	mu    sync.RWMutex
	items map[string]Factory
}

// New constructs an empty registry.
func New() *Registry {
	return &Registry{items: make(map[string]Factory)}
}

// Register adds (id, factory). Returns an error for empty id, nil factory,
// or duplicate id.
func (r *Registry) Register(id string, factory Factory) error {
	if id == "" {
		return errors.New("registry: adapter id must be non-empty")
	}
	if factory == nil {
		return fmt.Errorf("registry: factory for %q is nil", id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[id]; exists {
		return fmt.Errorf("registry: adapter %q already registered", id)
	}
	r.items[id] = factory
	return nil
}

// IDs returns the registered adapter ids, sorted for stable error messages.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.items))
	for id := range r.items {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// LoadConfig is the validated subset of the host's `adapter.*` config block
// the registry needs to pick and validate one bundled adapter.
type LoadConfig struct {
	// AdapterID is `adapter.id` from config.
	AdapterID string

	// VersionPin is the optional `adapter.version_pin`. Nil means "no pin"
	// and the version check is skipped.
	VersionPin *string

	// HostSpiVer is the SPI version the host implements. The registry
	// compares the adapter manifest's spi_version against this.
	HostSpiVer spi.SemVer
}

// Load constructs the adapter named by cfg, runs the LLD section 4
// validations, and returns the adapter or a structured error.
//
// Validation order matches the LLD:
//  1. UnknownAdapter — id not in registry
//  2. SpiMajorMismatch — adapter manifest spi major != host
//  3. ManifestIDMismatch — registry key != manifest.id
//  4. VersionPinUnsatisfiable — operator pin too strict for manifest support
//  5. Structural manifest validation (id pattern, vendor non-empty, etc.)
func (r *Registry) Load(_ context.Context, cfg LoadConfig) (spi.EhrAdapter, error) {
	r.mu.RLock()
	factory, ok := r.items[cfg.AdapterID]
	if !ok {
		r.mu.RUnlock()
		return nil, &UnknownAdapterError{
			Requested: cfg.AdapterID,
			Bundled:   r.idsLocked(),
		}
	}
	r.mu.RUnlock()

	adapter := factory()
	manifest := adapter.Manifest()

	if manifest.SpiVersion.Major != cfg.HostSpiVer.Major {
		return nil, &SpiMajorMismatchError{
			Host:    cfg.HostSpiVer,
			Adapter: manifest.SpiVersion,
		}
	}
	if !cfg.HostSpiVer.Compatible(manifest.SpiVersion) {
		return nil, &SpiMajorMismatchError{
			Host:    cfg.HostSpiVer,
			Adapter: manifest.SpiVersion,
		}
	}

	if manifest.ID != cfg.AdapterID {
		return nil, &ManifestIDMismatchError{
			Requested: cfg.AdapterID,
			Declared:  manifest.ID,
		}
	}

	if cfg.VersionPin != nil {
		if !manifest.SupportedEhrVersions.Satisfies(*cfg.VersionPin) {
			return nil, &VersionPinUnsatisfiableError{
				Pin:                  *cfg.VersionPin,
				SupportedEhrVersions: string(manifest.SupportedEhrVersions),
			}
		}
	}

	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("registry: invalid manifest for %q: %w", cfg.AdapterID, err)
	}

	// P1.10: stateful manifest validations.
	if err := validateConfigSchema(cfg.AdapterID, manifest.ConfigSchema); err != nil {
		return nil, err
	}
	if err := validateContributedTopicsUnique(cfg.AdapterID, manifest.ContributedTopics); err != nil {
		return nil, err
	}

	return adapter, nil
}

// validateConfigSchema compiles manifest.ConfigSchema as a JSON Schema
// and returns a structured error if it doesn't compile. An empty schema
// is allowed (treated as "no validation"); a non-empty schema must
// compile (P1.10).
func validateConfigSchema(adapterID string, raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	// jsonschema/v5 requires we add the resource to a compiler then
	// compile by URL. Use a synthetic URL keyed on the adapter id so a
	// compile error names the offending adapter.
	c := jsonschema.NewCompiler()
	url := "mem://adapter/" + adapterID + "/config_schema.json"
	if err := c.AddResource(url, bytes.NewReader(raw)); err != nil {
		return &ManifestConfigSchemaError{AdapterID: adapterID, Cause: err}
	}
	if _, err := c.Compile(url); err != nil {
		return &ManifestConfigSchemaError{AdapterID: adapterID, Cause: err}
	}
	return nil
}

// validateContributedTopicsUnique ensures every contributed topic
// declares a distinct canonical URL. A topic is rejected at the
// catalog layer if duplicates make it through, but per the LLD the
// adapter framework should refuse to start when the same adapter
// declares colliding URLs in its own manifest (P1.10).
func validateContributedTopicsUnique(adapterID string, topics [][]byte) error {
	if len(topics) < 2 {
		return nil
	}
	seen := map[string]string{}
	for i, raw := range topics {
		var doc struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			// Malformed topic JSON is its own LLD failure, but the catalog
			// will surface it; here we only enforce URL uniqueness when the
			// JSON parses.
			continue
		}
		if doc.URL == "" {
			continue
		}
		if prior, dup := seen[doc.URL]; dup {
			return &ManifestContributedTopicCollisionError{
				AdapterID: adapterID,
				URL:       doc.URL,
				FirstAt:   prior,
				SecondAt:  fmt.Sprintf("contributed_topics[%d]", i),
			}
		}
		seen[doc.URL] = fmt.Sprintf("contributed_topics[%d]", i)
	}
	return nil
}

// ManifestConfigSchemaError is returned when an adapter manifest's
// config_schema does not compile as a JSON Schema (P1.10).
type ManifestConfigSchemaError struct {
	AdapterID string
	Cause     error
}

func (e *ManifestConfigSchemaError) Error() string {
	return fmt.Sprintf("registry: adapter %q config_schema does not compile: %v", e.AdapterID, e.Cause)
}

func (e *ManifestConfigSchemaError) Unwrap() error { return e.Cause }

// ManifestContributedTopicCollisionError is returned when an adapter
// manifest declares two contributed topics with the same canonical URL
// (P1.10).
type ManifestContributedTopicCollisionError struct {
	AdapterID string
	URL       string
	FirstAt   string
	SecondAt  string
}

func (e *ManifestContributedTopicCollisionError) Error() string {
	return fmt.Sprintf("registry: adapter %q declares colliding contributed topic url %q (%s and %s)",
		e.AdapterID, e.URL, e.FirstAt, e.SecondAt)
}

// idsLocked is IDs() without re-acquiring the read lock. The caller must hold
// the lock or the call must be on a snapshot. Used by Load to surface a
// stable bundled list inside an error path.
func (r *Registry) idsLocked() []string {
	out := make([]string, 0, len(r.items))
	for id := range r.items {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ----- Structured errors per LLD section 4 -----

// UnknownAdapterError is returned when the configured adapter.id is not in
// the bundled-adapter registry.
type UnknownAdapterError struct {
	Requested string
	Bundled   []string
}

func (e *UnknownAdapterError) Error() string {
	return fmt.Sprintf("registry: unknown adapter %q (bundled: %v)", e.Requested, e.Bundled)
}

// SpiMajorMismatchError is returned when the adapter's manifest spi_version
// is not compatible with the host's.
type SpiMajorMismatchError struct {
	Host    spi.SemVer
	Adapter spi.SemVer
}

func (e *SpiMajorMismatchError) Error() string {
	return fmt.Sprintf("registry: SPI version mismatch — host=%s, adapter=%s", e.Host, e.Adapter)
}

// ManifestIDMismatchError is returned when the registry key and the
// manifest's declared id diverge (a misregistered factory).
type ManifestIDMismatchError struct {
	Requested string
	Declared  string
}

func (e *ManifestIDMismatchError) Error() string {
	return fmt.Sprintf("registry: manifest id mismatch — requested=%q, declared=%q",
		e.Requested, e.Declared)
}

// VersionPinUnsatisfiableError is returned when adapter.version_pin asks for
// support outside the adapter's manifest.supported_ehr_versions.
type VersionPinUnsatisfiableError struct {
	Pin                  string
	SupportedEhrVersions string
}

func (e *VersionPinUnsatisfiableError) Error() string {
	return fmt.Sprintf("registry: version_pin=%q not satisfiable by adapter supported_ehr_versions=%q",
		e.Pin, e.SupportedEhrVersions)
}
