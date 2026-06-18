// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// supportedFHIRTypes is the set of resource types the v0 schema and the
// built-in topic catalog will exercise. The mock rejects unknown types
// with 400 to surface harness misconfiguration loudly.
var supportedFHIRTypes = map[string]struct{}{
	"Patient":            {},
	"ServiceRequest":     {},
	"Encounter":          {},
	"Observation":        {},
	"DiagnosticReport":   {},
	"MedicationRequest":  {},
	"AllergyIntolerance": {},
	"DocumentReference":  {},
	// Subscription resources — used by the FHIR Subscriptions API tests
	// (subscriber registers a Subscription against the EHR's FHIR layer
	// in some scenarios). Cheap to allow.
	"Subscription":      {},
	"SubscriptionTopic": {},
}

// FHIRStore is a minimal in-memory FHIR REST store keyed by (type, id).
// Goroutine-safe; not partitioned.
type FHIRStore struct {
	mu       sync.RWMutex
	data     map[string]map[string]json.RawMessage // type → id → resource
	versions map[string]int                        // "type/id" → versionId counter
}

// NewFHIRStore returns an empty store.
func NewFHIRStore() *FHIRStore {
	return &FHIRStore{
		data:     map[string]map[string]json.RawMessage{},
		versions: map[string]int{},
	}
}

// Handler returns the http.Handler that serves the REST API.
func (s *FHIRStore) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *FHIRStore) serve(w http.ResponseWriter, req *http.Request) {
	parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	if len(parts) < 1 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rType := parts[0]
	if _, ok := supportedFHIRTypes[rType]; !ok {
		http.Error(w, "unsupported resource type: "+rType, http.StatusBadRequest)
		return
	}

	if len(parts) == 1 {
		// /{type} — search.
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSearch(w, req, rType)
		return
	}

	// /{type}/{id}
	id := parts[1]
	switch req.Method {
	case http.MethodGet:
		s.handleGet(w, rType, id)
	case http.MethodPut:
		s.handlePut(w, req, rType, id)
	case http.MethodDelete:
		s.handleDelete(w, rType, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *FHIRStore) handleGet(w http.ResponseWriter, rType, id string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket, ok := s.data[rType]
	if !ok {
		http.NotFound(w, &http.Request{})
		return
	}
	body, ok := bucket[id]
	if !ok {
		http.NotFound(w, &http.Request{})
		return
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *FHIRStore) handlePut(w http.ResponseWriter, req *http.Request, rType, id string) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	var resource map[string]any
	if err := json.Unmarshal(body, &resource); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := rType + "/" + id
	s.versions[key]++
	versionID := s.versions[key]

	resource["resourceType"] = rType
	resource["id"] = id
	meta, _ := resource["meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["versionId"] = strconv.Itoa(versionID)
	meta["lastUpdated"] = time.Now().UTC().Format(time.RFC3339Nano)
	resource["meta"] = meta

	out, err := json.Marshal(resource)
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, ok := s.data[rType]; !ok {
		s.data[rType] = map[string]json.RawMessage{}
	}
	wasNew := s.data[rType][id] == nil
	s.data[rType][id] = out

	w.Header().Set("Content-Type", "application/fhir+json")
	if wasNew {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.Write(out)
}

func (s *FHIRStore) handleDelete(w http.ResponseWriter, rType, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.data[rType]
	if _, ok := bucket[id]; !ok {
		http.NotFound(w, &http.Request{})
		return
	}
	delete(bucket, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *FHIRStore) handleSearch(w http.ResponseWriter, req *http.Request, rType string) {
	s.mu.RLock()
	bucket := s.data[rType]
	out := make([]json.RawMessage, 0, len(bucket))
	for _, body := range bucket {
		if matchesQuery(body, req.URL.Query()) {
			out = append(out, body)
		}
	}
	s.mu.RUnlock()

	entries := make([]map[string]any, 0, len(out))
	for _, body := range out {
		entries = append(entries, map[string]any{
			"resource": json.RawMessage(body),
		})
	}
	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "searchset",
		"total":        len(entries),
		"entry":        entries,
	}
	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(bundle)
}

// matchesQuery is a deliberately minimal exact-match filter for the
// scenarios v1 needs: /Patient?gender=male, /ServiceRequest?status=active.
// It walks the top-level fields of the resource and compares stringly. It
// does not implement FHIR search-parameter semantics (token, reference,
// chained, modifiers).
func matchesQuery(body []byte, q map[string][]string) bool {
	if len(q) == 0 {
		return true
	}
	var resource map[string]any
	if err := json.Unmarshal(body, &resource); err != nil {
		return false
	}
	for k, vs := range q {
		got, ok := resource[k]
		if !ok {
			return false
		}
		gotStr := stringifyJSONValue(got)
		any := false
		for _, v := range vs {
			if gotStr == v {
				any = true
				break
			}
		}
		if !any {
			return false
		}
	}
	return true
}

func stringifyJSONValue(v any) string {
	switch s := v.(type) {
	case string:
		return s
	default:
		b, _ := json.Marshal(v)
		return strings.Trim(string(b), `"`)
	}
}
