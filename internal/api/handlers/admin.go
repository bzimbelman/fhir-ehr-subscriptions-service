// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
// when Deps.AdminToken is non-empty AND its length is at least
// MinAdminTokenBytes; otherwise the entire admin surface is disabled
// at the router layer (the routes do not exist, requests 404).
//
// Story #92: rejecting a too-short token at the router layer (rather
// than 401-ing every request) keeps the surface invisible to a probe
// when the operator misconfigures the secret. Production wiring
// (cmd/fhir-subs) ALSO returns a startup error when the token is
// non-empty-but-too-short so the binary refuses to boot rather than
// silently disabling the surface.
//
// The admin surface is OUTSIDE the SMART/OAuth path on purpose: an
// on-call operator triaging an incident does not have a client_id /
// JWT lifecycle. The shared secret is the gate; network-level scoping
// (allow-list, private VPC, mTLS reverse proxy) is the operator's
// responsibility.
func RegisterAdminRoutes(r chi.Router, d Deps) {
	if len(d.AdminToken) < MinAdminTokenBytes {
		return
	}
	prefix := d.AdminPathPrefix
	if prefix == "" {
		prefix = DefaultAdminPathPrefix
	}
	a := &adminServer{
		deps:        d,
		actorDigest: adminTokenDigest(d.AdminToken),
	}
	r.Route(prefix, func(r chi.Router) {
		r.Use(adminAuthMiddleware(d.AdminToken))
		if d.AdminRateLimit != nil {
			r.Use(d.AdminRateLimit.Middleware())
		}
		r.Get("/topics", a.listTopics)
		r.Get("/subscriptions", a.listSubscriptions)
		r.Get("/dead_letters", a.listDeadLetters)
		if d.SupervisorStatus != nil {
			r.Get("/supervisor/status", a.listSupervisorStatus)
		}
	})
}

// adminTokenDigest hashes the configured admin token with SHA-256 and
// returns the first 16 hex chars. Cached on adminServer so each
// authenticated request does not re-hash the secret. The digest is the
// audit row's actor identifier — it is greppable in audit_log without
// leaking the token itself.
func adminTokenDigest(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:8]) // 8 bytes -> 16 hex chars
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
	// actorDigest is the SHA-256-truncated identifier of the configured
	// admin token, written to audit_log so triage incidents are greppable
	// without exposing the secret itself (story #92).
	actorDigest string
}

// auditAction* constants are the action strings written to audit_log for
// each admin endpoint. Keeping them centralized makes the cross-handler
// "every admin op emits audit" invariant easy to audit (story #92).
const (
	auditActionAdminTopicsList        = "admin.topics.list"
	auditActionAdminSubscriptionsList = "admin.subscriptions.list"
	auditActionAdminDeadLettersList   = "admin.dead_letters.list"
)

// auditOutcome* are the outcome strings the admin handlers record.
const (
	auditOutcomeSuccess    = "success"
	auditOutcomeBadRequest = "bad_request"
	auditOutcomeError      = "internal_error"
)

// emitAdminAudit writes one audit_log row from an admin handler. The
// canonical body carries the actor digest, the request path, and the
// raw query string so post-incident triage can attribute the request
// without the secret. Errors from Append are intentionally swallowed:
// audit-log persistence failures must not turn an admin read into a 500
// — the operator surface is for triage and degraded telemetry is still
// better than no telemetry. The error is dropped; future revisions may
// log it once a slog.Logger is plumbed onto adminServer.
func (a *adminServer) emitAdminAudit(r *http.Request, action, target, outcome string) {
	if a.deps.Audit == nil {
		return
	}
	canonical, _ := json.Marshal(map[string]string{
		"actor":  "admin-token:" + a.actorDigest,
		"path":   r.URL.Path,
		"query":  r.URL.RawQuery,
		"method": r.Method,
	})
	_ = a.deps.Audit.Append(r.Context(), action, target, outcome, nil, canonical)
}

// listTopics returns the active topic catalog. The body is small — the
// catalog typically holds <100 rows in production deployments.
//
// Story #92 / audit findings 7, 117: every admin handler emits an
// audit_log row. The append happens BEFORE the response is written so
// a crashing handler still leaves a triage trail.
func (a *adminServer) listTopics(w http.ResponseWriter, r *http.Request) {
	rows, err := a.deps.Topics.ListActive(r.Context())
	if err != nil {
		a.emitAdminAudit(r, auditActionAdminTopicsList, "topics", auditOutcomeError)
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
	a.emitAdminAudit(r, auditActionAdminTopicsList, "topics", auditOutcomeSuccess)
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}

// listSubscriptions returns subscriptions filtered by `clientId`. The
// query parameter is required so operators cannot accidentally page
// the entire fleet.
//
// Story #92: validation failures and the success path both emit audit;
// the outcome distinguishes them so post-incident triage can detect a
// flood of malformed admin probes.
func (a *adminServer) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	clientID := strings.TrimSpace(r.URL.Query().Get("clientId"))
	if clientID == "" {
		a.emitAdminAudit(r, auditActionAdminSubscriptionsList, "subscriptions", auditOutcomeBadRequest)
		writeAdminJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "bad_request",
			"message": "query parameter 'clientId' is required",
		})
		return
	}
	rows, err := a.deps.Subscriptions.ListByClient(r.Context(), clientID)
	if err != nil {
		a.emitAdminAudit(r, auditActionAdminSubscriptionsList, "subscriptions:"+clientID, auditOutcomeError)
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
	a.emitAdminAudit(r, auditActionAdminSubscriptionsList, "subscriptions:"+clientID, auditOutcomeSuccess)
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"clientId": clientID,
		"items":    out,
		"total":    len(out),
	})
}

// listDeadLetters returns the most recent N dead-letter rows. The
// `payload_redacted` blob is intentionally NOT returned: it is encrypted
// at rest and decryption is offline, not part of the triage flow.
//
// Story #92: the `limit` query parameter is parsed with parseAdminLimit
// — strict ASCII-digit-only parsing that rejects leading +, unicode
// digits, internal whitespace, hex, trailing garbage, and int64
// overflow. Surrounding whitespace is trimmed for caller convenience.
// Values above MaxAdminDeadLetterLimit are clamped (not rejected) so a
// well-meaning operator who pastes "1000" still gets useful output.
func (a *adminServer) listDeadLetters(w http.ResponseWriter, r *http.Request) {
	if a.deps.DeadLetters == nil {
		writeAdminJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "unavailable",
			"message": "dead-letter store not wired",
		})
		return
	}
	limit := DefaultAdminDeadLetterLimit
	q := r.URL.Query()
	if q.Has("limit") {
		raw := strings.TrimSpace(q.Get("limit"))
		v, err := parseAdminLimit(raw)
		if err != nil {
			a.emitAdminAudit(r, auditActionAdminDeadLettersList, "dead_letters", auditOutcomeBadRequest)
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
		a.emitAdminAudit(r, auditActionAdminDeadLettersList, "dead_letters", auditOutcomeError)
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
	a.emitAdminAudit(r, auditActionAdminDeadLettersList, "dead_letters", auditOutcomeSuccess)
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
		"limit": limit,
	})
}

// parseAdminLimit is the strict ASCII-digit-only parser the admin
// dead-letters endpoint uses to validate `?limit=`. Story #92 acceptance
// criterion 5: rejecting leading +, unicode digits, internal whitespace,
// hex form, trailing garbage, and int64 overflow keeps the surface
// resistant to subtle injection probes that bypass strconv.Atoi's
// permissive form (e.g. "+42" parses fine in some langs but signals an
// attacker who's mapping out the parser).
//
// OP #223: clamp at MaxAdminDeadLetterLimit BEFORE any parsing-overflow
// can occur. A raw string strictly longer than the digit width of
// MaxAdminDeadLetterLimit is rejected on length alone — the parser never
// runs strconv.Atoi on a candidate that could overflow int64 (or the
// admin handler's int return). The digit-only loop is preserved so a
// length-fitting input still rejects sign chars, unicode digits, hex
// prefixes, and trailing garbage.
//
// Surrounding whitespace is trimmed by the caller for operator
// convenience; this function sees only the trimmed value and rejects
// anything but [0-9]+ within the maxDigits length budget.
func parseAdminLimit(raw string) (int, error) {
	if raw == "" {
		return 0, errors.New("admin limit: empty")
	}
	maxDigits := admimDeadLetterLimitDigits
	if len(raw) > maxDigits {
		return 0, fmt.Errorf("admin limit: too long (max %d digits)", maxDigits)
	}
	v := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			// Rejects sign characters, unicode digits, hex prefix,
			// internal whitespace, and trailing garbage in one pass.
			return 0, errors.New("admin limit: non-ascii-digit byte (value redacted)")
		}
		v = v*10 + int(r-'0')
	}
	if v <= 0 {
		return 0, errors.New("admin limit: non-positive")
	}
	return v, nil
}

// admimDeadLetterLimitDigits is the digit width of MaxAdminDeadLetterLimit.
// Computed once at init so parseAdminLimit's hot path stays
// allocation-free.
var admimDeadLetterLimitDigits = func() int {
	d, n := 0, MaxAdminDeadLetterLimit
	for n > 0 {
		d++
		n /= 10
	}
	if d == 0 {
		d = 1
	}
	return d
}()

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
