// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// loadTopicSources walks dir non-recursively and returns every *.json
// file as one Operator-precedence RawTopic. An empty or missing dir
// yields catalog.Sources{} without error so the binary still boots.
//
// Per-file read failures are returned as errors only when the directory
// itself is unreadable; an individual unreadable file is reported as a
// rejection later by catalog.Load (origin=path, reason=read error).
func loadTopicSources(dir string) (catalog.Sources, error) {
	if strings.TrimSpace(dir) == "" {
		return catalog.Sources{}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Missing dir is non-fatal: the operator may not have mounted
		// the volume yet (rolling update race). Other errors (perm,
		// I/O) are surfaced so an operator misconfiguration fails loud.
		if errors.Is(err, os.ErrNotExist) {
			return catalog.Sources{}, nil
		}
		return catalog.Sources{}, fmt.Errorf("topics: read dir %q: %w", dir, err)
	}
	// Sort for deterministic load order so rejection / override
	// diagnostics are stable across pod restarts.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := catalog.Sources{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		body, readErr := os.ReadFile(path) //nolint:gosec // operator-supplied dir is intended.
		if readErr != nil {
			return catalog.Sources{}, fmt.Errorf("topics: read %q: %w", path, readErr)
		}
		out.Operator = append(out.Operator, catalog.RawTopic{
			Origin: path,
			Bytes:  body,
		})
	}
	return out, nil
}
