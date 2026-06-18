// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package catalog_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// B-23: Topics that reference search parameters outside the supported set
// must be rejected at catalog load — not silently never match.

func TestLoadRejectsUnsupportedSearchParameter(t *testing.T) {
	t.Parallel()

	// performer is a FHIR search param but not in the matcher's supported
	// extractor set; the topic must be rejected at load time.
	bad := strings.Replace(minimalTopic,
		`"current": "status=active"`,
		`"current": "performer=Practitioner/123"`, 1)

	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/performer", Bytes: []byte(bad)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(report.Rejected) != 1 {
		t.Fatalf("expected 1 rejection for unsupported parameter; got %#v", report.Rejected)
	}
	if !strings.Contains(report.Rejected[0].Reason, "performer") {
		t.Errorf("rejection should name the offending parameter; got %q", report.Rejected[0].Reason)
	}
	if !strings.Contains(strings.ToLower(report.Rejected[0].Reason), "unsupported") &&
		!strings.Contains(strings.ToLower(report.Rejected[0].Reason), "unknown") {
		t.Errorf("rejection reason should flag unsupported/unknown param; got %q", report.Rejected[0].Reason)
	}
	if got := len(report.Catalog.All()); got != 0 {
		t.Errorf("expected 0 active topics, got %d", got)
	}
}

func TestLoadRejectsUnsupportedFilterByParameter(t *testing.T) {
	t.Parallel()

	// canFilterBy.filterParameter is the operator-visible API; if the
	// matcher can't evaluate it, the catalog should reject the topic.
	bad := strings.Replace(minimalTopic,
		`"filterParameter": "patient"`,
		`"filterParameter": "encounter"`, 1)

	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/encounter-filter", Bytes: []byte(bad)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(report.Rejected) != 1 {
		t.Fatalf("expected 1 rejection; got %#v", report.Rejected)
	}
	if !strings.Contains(report.Rejected[0].Reason, "encounter") {
		t.Errorf("rejection should name the offending filter parameter; got %q", report.Rejected[0].Reason)
	}
}

func TestLoadAcceptsAllSupportedSearchParameters(t *testing.T) {
	t.Parallel()

	// All parameters in catalog.SupportedSearchParameters() must be
	// accepted (each in turn) without rejection.
	for _, p := range catalog.SupportedSearchParameters() {
		topicJSON := strings.Replace(minimalTopic,
			`"current": "status=active"`,
			`"current": "`+p+`=anything"`, 1)
		// also drop the canFilterBy block to isolate the queryCriteria
		// check.
		topicJSON = strings.Replace(topicJSON,
			`"canFilterBy": [{
    "resource": "ServiceRequest",
    "filterParameter": "patient"
  }],`, "", 1)

		report, err := catalog.Load(catalog.Sources{
			BuiltIn: []catalog.RawTopic{
				{Origin: "builtin/" + p, Bytes: []byte(topicJSON)},
			},
		})
		if err != nil {
			t.Fatalf("Load(%s): %v", p, err)
		}
		if len(report.Rejected) != 0 {
			t.Errorf("supported param %q rejected: %#v", p, report.Rejected)
		}
	}
}

// B-25: The loader must not lose information about why a topic was
// rejected, and the rejected topic must NOT appear in Catalog.All().

func TestLoadRejectedTopicAbsentFromCatalog(t *testing.T) {
	t.Parallel()

	bad := strings.Replace(minimalTopic,
		`"current": "status=active"`,
		`"current": "performer=Practitioner/123"`, 1)

	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/performer", Bytes: []byte(bad)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(report.Rejected); got != 1 {
		t.Fatalf("expected 1 rejection, got %d", got)
	}
	if got := len(report.Catalog.All()); got != 0 {
		t.Errorf("rejected topic must not appear in Catalog.All(); got %d topics", got)
	}
}

// B-25 (continued): Conflict between built-in and operator on the same
// (url, version) where the operator override fails to compile must fall
// back to the built-in topic with a structured override record.

func TestLoadFallsBackToBuiltInWhenOperatorOverrideRejected(t *testing.T) {
	t.Parallel()

	// Built-in: valid topic.
	builtIn := minimalTopic
	// Operator override: same url/version, but uses an unsupported
	// parameter so it will be rejected at compile time.
	operatorOverride := strings.Replace(minimalTopic,
		`"current": "status=active"`,
		`"current": "performer=Practitioner/123"`, 1)

	report, err := catalog.Load(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/order-changed", Bytes: []byte(builtIn)},
		},
		Operator: []catalog.RawTopic{
			{Origin: "/etc/topics/order-changed.json", Bytes: []byte(operatorOverride)},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(report.Rejected); got != 1 {
		t.Errorf("expected the failing operator override to be rejected; got %#v", report.Rejected)
	}
	all := report.Catalog.All()
	if len(all) != 1 {
		t.Fatalf("expected the built-in to remain active after operator override fails; got %d topics", len(all))
	}
	if all[0].Source != catalog.SourceBuiltIn {
		t.Errorf("expected built-in source after override fallback; got %v", all[0].Source)
	}
	if got := len(report.Overridden); got == 0 {
		t.Errorf("expected an Overridden record for the failed override")
	} else {
		o := report.Overridden[0]
		if o.URL == "" || o.Version == "" {
			t.Errorf("overridden record should carry url+version; got %#v", o)
		}
	}
}

// B-25: Strict mode must surface load-time rejections as a fatal error.

func TestLoadStrictModeRejectsAtStartup(t *testing.T) {
	t.Parallel()

	bad := strings.Replace(minimalTopic,
		`"current": "status=active"`,
		`"current": "performer=Practitioner/123"`, 1)

	_, err := catalog.LoadStrict(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/performer", Bytes: []byte(bad)},
		},
	})
	if err == nil {
		t.Fatal("LoadStrict should return a non-nil error when any topic is rejected")
	}
	if !strings.Contains(err.Error(), "performer") {
		t.Errorf("strict-mode error should name the bad parameter; got %v", err)
	}
}

// LoadStrict must succeed when every topic compiles cleanly.
func TestLoadStrictModeAcceptsValidCatalog(t *testing.T) {
	t.Parallel()

	report, err := catalog.LoadStrict(catalog.Sources{
		BuiltIn: []catalog.RawTopic{
			{Origin: "builtin/order-changed", Bytes: []byte(minimalTopic)},
		},
	})
	if err != nil {
		t.Fatalf("LoadStrict on a valid catalog must not error: %v", err)
	}
	if got := len(report.Catalog.All()); got != 1 {
		t.Errorf("strict load: expected 1 topic, got %d", got)
	}
}
