// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// DefaultAdminPathPrefix is the URL prefix the admin routes mount at
// when Deps.AdminPathPrefix is empty (P1.6).
const DefaultAdminPathPrefix = "/admin"

// DefaultAdminDeadLetterLimit is the response cap for /admin/dead_letters
// when the caller does not pass `limit`. 50 fits one screenful and stays
// well under the 1MB response budget operators expect.
const DefaultAdminDeadLetterLimit = 50

// MaxAdminDeadLetterLimit is the hard cap an operator can request — 500
// keeps the JSON response bounded so an admin terminal cannot accidentally
// DOS the bridge.
const MaxAdminDeadLetterLimit = 500

// MinAdminTokenBytes is the floor on Deps.AdminToken length. Operators
// are expected to pass a high-entropy secret; <32 chars is rejected at
// wire-up to keep brute-force off the table.
const MinAdminTokenBytes = 32

// DeadLettersListStore is the narrow read-only interface
// /admin/dead_letters needs from the dead_letters table. Mirrors the
// other Store interfaces' pattern (concrete pgx pool wired by cmd).
type DeadLettersListStore interface {
	ListRecent(ctx context.Context, limit int) ([]repos.DeadLetterRow, error)
}

// RegisterAdminRoutes mounts the read-only admin operator surface
// behind a shared-secret bearer-token middleware (P1.6). Mounted only
// when Deps.AdminToken is non-empty; otherwise the entire admin
// surface is disabled at the router layer.
//
// The admin surface is OUTSIDE the SMART/OAuth path on purpose: an
// on-call operator triaging an incident does not have a client_id /
// JWT lifecycle. The shared secret is the gate; network-level scoping
// (allow-list, private VPC, mTLS reverse proxy) is the operator's
// responsibility.
func RegisterAdminRoutes(r chi.Router, d Deps) {
	if d.AdminToken == "" {
		return
	}
	prefix := d.AdminPathPrefix
	if prefix == "" {
		prefix = DefaultAdminPathPrefix
	}
	a := &adminServer{deps: d}
	r.Route(prefix, func(r chi.Router) {
		r.Use(adminAuthMiddleware(d.AdminToken))
		r.Get("/topics", a.listTopics)
		r.Get("/subscriptions", a.listSubscriptions)
		r.Get("/dead_letters", a.listDeadLetters)
	})
}

// adminAuthMiddleware constant-time compares the Authorization header
// against the configured token. Wrong / missing tokens get 401 with a
// minimal body so a probe does not learn whether the surface exists.
func adminAuthMiddleware(token string) func(http.Handler) http.Handler {
	want := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(h, prefix) {
				adminUnauthorized(w)
				return
			}
			got := []byte(strings.TrimPrefix(h, prefix))
			if subtle.ConstantTimeCompare(got, want) != 1 {
				adminUnauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func adminUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

// adminServer holds the dep handles the admin handlers need.
type adminServer struct {
	deps Deps
}

// listTopics returns the active topic catalog. The body is small — the
// catalog typically holds <100 rows in production deployments.
func (a *adminServer) listTopics(w http.ResponseWriter, r *http.Request) {
	rows, err := a.deps.Topics.ListActive(r.Context())
	if err != nil {
		adminInternalError(w, "list_topics_failed")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		out = append(out, map[string]any{
			"url":     rows[i].URL,
			"version": rows[i].Version,
			"status":  rows[i].Status,
			"title":   rows[i].Title,
		})
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}

// listSubscriptions returns subscriptions filtered by `clientId`. The
// query parameter is required so operators cannot accidentally page
// the entire fleet.
func (a *adminServer) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	clientID := strings.TrimSpace(r.URL.Query().Get("clientId"))
	if clientID == "" {
		writeAdminJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": "query parameter 'clientId' is required",
		})
		return
	}
	rows, err := a.deps.Subscriptions.ListByClient(r.Context(), clientID)
	if err != nil {
		adminInternalError(w, "list_subscriptions_failed")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		s := &rows[i]
		out = append(out, map[string]any{
			"id":                           s.ID.String(),
			"clientId":                     s.ClientID,
			"status":                       string(s.Status),
			"topicUrl":                     s.TopicURL,
			"channelType":                  s.ChannelType,
			"endpoint":                     s.Endpoint,
			"content":                      s.Content,
			"createdAt":                    s.CreatedAt,
			"updatedAt":                    s.UpdatedAt,
			"lastError":                    s.Error,
			"eventsSinceSubscriptionStart": s.EventsSinceSubscriptionStart,
		})
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"clientId": clientID,
		"items":    out,
		"total":    len(out),
	})
}

// listDeadLetters returns the most recent N dead-letter rows. The
// `payload_redacted` blob is intentionally NOT returned: it is encrypted
// at rest and decryption is offline, not part of the triage flow.
func (a *adminServer) listDeadLetters(w http.ResponseWriter, r *http.Request) {
	if a.deps.DeadLetters == nil {
		writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "unavailable",
			"message": "dead-letter store not wired",
		})
		return
	}
	limit := DefaultAdminDeadLetterLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeAdminJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "bad_request",
				"message": "query parameter 'limit' must be a positive integer",
			})
			return
		}
		if v > MaxAdminDeadLetterLimit {
			v = MaxAdminDeadLetterLimit
		}
		limit = v
	}
	rows, err := a.deps.DeadLetters.ListRecent(r.Context(), limit)
	if err != nil {
		adminInternalError(w, "list_dead_letters_failed")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		dl := &rows[i]
		row := map[string]any{
			"id":          dl.ID.String(),
			"kind":        dl.Kind,
			"sourceTable": dl.SourceTable,
			"sourceId":    dl.SourceID.String(),
			"reason":      dl.Reason,
			"errorDetail": string(dl.ErrorDetail),
			"createdAt":   dl.CreatedAt,
		}
		if dl.SubscriptionID != nil {
			row["subscriptionId"] = dl.SubscriptionID.String()
		}
		if dl.CorrelationID != nil {
			row["correlationId"] = dl.CorrelationID.String()
		}
		out = append(out, row)
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
		"limit": limit,
	})
}

func adminInternalError(w http.ResponseWriter, code string) {
	writeAdminJSON(w, http.StatusInternalServerError, map[string]any{
		"error": code,
	})
}

func writeAdminJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
