// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestCapabilityStatement_SearchTypeAdvertisesSearchParams covers OP
// #192. FHIR R5 §5.1.5.1: when a CapabilityStatement declares
// interaction:search-type for a resource it MUST also list every
// search parameter the server accepts. The Subscription handler
// accepts _count and _cursor; both must show up under
// rest[0].resource[Subscription].searchParam[].
func TestCapabilityStatement_SearchTypeAdvertisesSearchParams(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var cs map[string]any
	if err := json.Unmarshal(body, &cs); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}

	subResource := findResource(t, cs, "Subscription")
	hasSearchType := resourceHasInteraction(subResource, "search-type")
	if !hasSearchType {
		t.Fatalf("Subscription resource missing interaction:search-type — fixture invariant; got %v", subResource["interaction"])
	}
	params, _ := subResource["searchParam"].([]any)
	if len(params) == 0 {
		t.Fatalf("Subscription declares search-type but emits no searchParam[] entries; OP #192 / FHIR R5 §5.1.5.1 violation")
	}
	wantNames := map[string]bool{"_count": false, "_cursor": false}
	for _, raw := range params {
		p, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("searchParam entry is not an object: %T", raw)
		}
		name, _ := p["name"].(string)
		typ, _ := p["type"].(string)
		if name == "" {
			t.Errorf("searchParam entry missing name: %+v", p)
		}
		if typ == "" {
			t.Errorf("searchParam %q missing type", name)
		}
		if _, want := wantNames[name]; want {
			wantNames[name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("Subscription.searchParam missing %q (handler accepts it on /Subscription)", name)
		}
	}

	// OP #192 second leg: SubscriptionTopic accepts no parameters, so
	// it MUST NOT advertise interaction:search-type. Either drop the
	// declaration or add searchParam[]. We chose the drop.
	topicResource := findResource(t, cs, "SubscriptionTopic")
	if resourceHasInteraction(topicResource, "search-type") {
		params, _ := topicResource["searchParam"].([]any)
		if len(params) == 0 {
			t.Errorf("SubscriptionTopic declares search-type with zero searchParam[] entries; OP #192 requires either dropping the interaction or listing the accepted params")
		}
	}
}

// TestCapabilityStatement_ExtensionURLsAreAbsolute covers OP #193.
// FHIR R5: Element.extension.url MUST be an absolute URI. HAPI and
// Inferno reject bare strings.
func TestCapabilityStatement_ExtensionURLsAreAbsolute(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var cs map[string]any
	if err := json.Unmarshal(body, &cs); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	exts, _ := cs["extension"].([]any)
	if len(exts) == 0 {
		t.Fatalf("CapabilityStatement.extension empty; expected supported-channels/supported-topics/supported-channel-set/supported-fhir-versions entries")
	}
	for i, raw := range exts {
		ext, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("extension[%d] is not an object: %T", i, raw)
		}
		u, _ := ext["url"].(string)
		if u == "" {
			t.Errorf("extension[%d].url empty", i)
			continue
		}
		assertAbsoluteURI(t, u, "extension[%d].url=%q", i, u)
	}
}

func findResource(t *testing.T, cs map[string]any, typ string) map[string]any {
	t.Helper()
	rest, _ := cs["rest"].([]any)
	if len(rest) == 0 {
		t.Fatalf("rest empty")
	}
	r0, _ := rest[0].(map[string]any)
	resources, _ := r0["resource"].([]any)
	for _, raw := range resources {
		r, _ := raw.(map[string]any)
		if t2, _ := r["type"].(string); t2 == typ {
			return r
		}
	}
	t.Fatalf("rest[0].resource[%s] not found", typ)
	return nil
}

func resourceHasInteraction(resource map[string]any, code string) bool {
	ix, _ := resource["interaction"].([]any)
	for _, raw := range ix {
		entry, _ := raw.(map[string]any)
		if c, _ := entry["code"].(string); c == code {
			return true
		}
	}
	return false
}

func assertAbsoluteURI(t *testing.T, u string, msgFmt string, args ...any) {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Errorf("parse %q: %v ("+msgFmt+")", append([]any{u, err}, args...)...)
		return
	}
	if !parsed.IsAbs() {
		t.Errorf("not absolute: %q ("+msgFmt+")", append([]any{u}, args...)...)
		return
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		t.Errorf("scheme %q not http(s) ("+msgFmt+")", append([]any{parsed.Scheme}, args...)...)
	}
}
