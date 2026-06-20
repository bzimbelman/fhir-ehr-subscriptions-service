// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"math"
	"os"
	"testing"
)

func TestParseFunc_AggregatesByPackage(t *testing.T) {
	t.Parallel()

	in := `github.com/x/foo/a.go:12:	A	100.0%
github.com/x/foo/a.go:30:	B	50.0%
github.com/x/bar/b.go:7:	C	90.0%
total:	(statements)	77.4%
`
	got, err := parseFunc(in)
	if err != nil {
		t.Fatalf("parseFunc: %v", err)
	}
	want := map[string]float64{
		"github.com/x/foo": 75.0,
		"github.com/x/bar": 90.0,
	}
	if len(got) != len(want) {
		t.Fatalf("packages: got %v, want %v", got, want)
	}
	for k, w := range want {
		if math.Abs(got[k]-w) > 1e-6 {
			t.Errorf("pkg %s: got %.4f, want %.4f", k, got[k], w)
		}
	}
}

func TestLoadThresholds_DefaultsTo80(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/thresholds.json"
	if err := writeFile(path, `{"packages":{}}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadThresholds(path)
	if err != nil {
		t.Fatalf("loadThresholds: %v", err)
	}
	if cfg.Default != 80 {
		t.Errorf("default: got %v, want 80", cfg.Default)
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
