// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package catalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// TestDemoTopicCatalogLoads guards the demo topics under demo/topics/
// against drift in the catalog's accepted subset. Every JSON file there
// is meant to be loadable by the production loader so the demo
// publisher / subscriber walkthroughs (Gaps 3 and 4) don't ship topics
// that silently fail at runtime. If this test fails, either fix the
// demo file or update the catalog rules — never quietly broaden the
// demo to use unsupported features.
func TestDemoTopicCatalogLoads(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", "..", "demo", "topics")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read demo/topics: %v", err)
	}

	var raws []catalog.RawTopic
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %q: %v", path, readErr)
		}
		raws = append(raws, catalog.RawTopic{Origin: path, Bytes: body})
	}
	if len(raws) == 0 {
		t.Fatal("no demo/topics/*.json files found; the demo catalog must ship at least one topic")
	}

	report, err := catalog.Load(catalog.Sources{Operator: raws})
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	if len(report.Rejected) != 0 {
		for _, r := range report.Rejected {
			t.Errorf("demo topic rejected: origin=%s url=%s reason=%s", r.Origin, r.URL, r.Reason)
		}
		t.FailNow()
	}
	if report.Catalog == nil {
		t.Fatal("nil catalog")
	}
	if got, want := len(report.Catalog.All()), len(raws); got != want {
		t.Fatalf("loaded topic count = %d, want %d (one per file)", got, want)
	}
}
