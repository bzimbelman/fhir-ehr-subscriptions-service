// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package merger_test

import (
	"reflect"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/merger"
)

// TestPrecedenceCLIOverEverything: CLI > env > file > defaults. Last argument
// wins per Merge semantics; the caller must pass layers in that order.
func TestPrecedenceCLIOverEverything(t *testing.T) {
	t.Parallel()
	defaults := map[string]interface{}{
		"deployment": map[string]interface{}{"log_level": "info", "facility_id": "from-defaults"},
	}
	file := map[string]interface{}{
		"deployment": map[string]interface{}{"facility_id": "from-file"},
	}
	env := map[string]interface{}{
		"deployment": map[string]interface{}{"log_level": "warn"},
	}
	cli := map[string]interface{}{
		"deployment": map[string]interface{}{"log_level": "debug"},
	}
	got := merger.Merge(defaults, file, env, cli)
	want := map[string]interface{}{
		"deployment": map[string]interface{}{
			"log_level":   "debug",     // cli wins
			"facility_id": "from-file", // file wins (no env/cli override)
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge mismatch:\n got=%#v\nwant=%#v", got, want)
	}
}

// TestFieldByFieldMerge: nested maps merge deeply, not by replacement.
func TestFieldByFieldMerge(t *testing.T) {
	t.Parallel()
	low := map[string]interface{}{
		"server": map[string]interface{}{
			"http": map[string]interface{}{
				"bind": "0.0.0.0:8443",
				"tls": map[string]interface{}{
					"cert_file": "/etc/fhir-subs/tls.crt",
					"key_file":  "/etc/fhir-subs/tls.key",
				},
			},
		},
	}
	high := map[string]interface{}{
		"server": map[string]interface{}{
			"http": map[string]interface{}{
				"tls": map[string]interface{}{
					"cert_file": "/run/secrets/tls.crt",
				},
			},
		},
	}
	got := merger.Merge(low, high)
	wantTLS := map[string]interface{}{
		"cert_file": "/run/secrets/tls.crt", // overridden
		"key_file":  "/etc/fhir-subs/tls.key",
	}
	srv := got["server"].(map[string]interface{})
	http := srv["http"].(map[string]interface{})
	if http["bind"] != "0.0.0.0:8443" {
		t.Fatalf("bind preserved? got=%v", http["bind"])
	}
	if !reflect.DeepEqual(http["tls"], wantTLS) {
		t.Fatalf("tls merge:\n got=%#v\nwant=%#v", http["tls"], wantTLS)
	}
}

// TestArrayReplace: arrays/slices are replaced, not element-wise merged. This
// matches the architecture's expectation that an operator overriding
// trusted_issuers replaces the list wholesale, not appends.
func TestArrayReplace(t *testing.T) {
	t.Parallel()
	low := map[string]interface{}{
		"auth": map[string]interface{}{
			"trusted_issuers": []interface{}{
				map[string]interface{}{"issuer": "https://a.example"},
				map[string]interface{}{"issuer": "https://b.example"},
			},
		},
	}
	high := map[string]interface{}{
		"auth": map[string]interface{}{
			"trusted_issuers": []interface{}{
				map[string]interface{}{"issuer": "https://override.example"},
			},
		},
	}
	got := merger.Merge(low, high)
	auth := got["auth"].(map[string]interface{})
	tis := auth["trusted_issuers"].([]interface{})
	if len(tis) != 1 {
		t.Fatalf("expected len 1 (replace), got %d", len(tis))
	}
	if tis[0].(map[string]interface{})["issuer"] != "https://override.example" {
		t.Fatalf("override mismatch: %#v", tis[0])
	}
}

// TestZeroLayers: Merge with no layers returns empty map.
func TestZeroLayers(t *testing.T) {
	t.Parallel()
	got := merger.Merge()
	if len(got) != 0 {
		t.Fatalf("expected empty, got %#v", got)
	}
}

// TestEmptyLayerNoOp: an empty layer doesn't unset prior values.
func TestEmptyLayerNoOp(t *testing.T) {
	t.Parallel()
	low := map[string]interface{}{"deployment": map[string]interface{}{"log_level": "info"}}
	high := map[string]interface{}{}
	got := merger.Merge(low, high)
	if got["deployment"].(map[string]interface{})["log_level"] != "info" {
		t.Fatalf("empty high should not erase low")
	}
}

// TestTypeMismatchTakesHigherPrecedence: when a higher layer disagrees on type,
// the higher wins.
func TestTypeMismatchTakesHigherPrecedence(t *testing.T) {
	t.Parallel()
	low := map[string]interface{}{"x": map[string]interface{}{"y": "value"}}
	high := map[string]interface{}{"x": "not-a-map"}
	got := merger.Merge(low, high)
	if got["x"] != "not-a-map" {
		t.Fatalf("higher precedence should win on type clash; got %#v", got["x"])
	}
}
