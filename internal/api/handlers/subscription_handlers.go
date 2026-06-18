// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/fhirerror"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/schemas"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// recordCreated, recordUpdated, recordDeleted, recordWsTokenIssued, and
// recordValidationFailure are nil-safe metric helpers used by every
// handler.
func (s *server) recordCreated() {
	if s.deps.Metrics != nil {
		s.deps.Metrics.RecordSubscriptionCreated()
	}
}
func (s *server) recordUpdated() {
	if s.deps.Metrics != nil {
		s.deps.Metrics.RecordSubscriptionUpdated()
	}
}
func (s *server) recordDeleted() {
	if s.deps.Metrics != nil {
		s.deps.Metrics.RecordSubscriptionDeleted()
	}
}
func (s *server) recordWsTokenIssued() {
	if s.deps.Metrics != nil {
		s.deps.Metrics.RecordWSBindingTokenIssued()
	}
}
func (s *server) recordValidationFailure(kind string) {
	if s.deps.Metrics != nil {
		s.deps.Metrics.RecordValidationFailure(kind)
	}
}

// requireScopes returns true if the principal carries every needed
// scope; otherwise it writes a 403 OperationOutcome and returns false.
func requireScopes(w http.ResponseWriter, p *auth.Principal, needed ...string) bool {
	if p == nil {
		fhirerror.WriteError(w, http.StatusUnauthorized, fhirerror.CodeLogin,
			"missing principal")
		return false
	}
	if !p.HasAllScopes(needed...) {
		fhirerror.WriteError(w, http.StatusForbidden, fhirerror.CodeForbidden,
			"insufficient scope")
		return false
	}
	return true
}

func mustPrincipal(w http.ResponseWriter, r *http.Request) *auth.Principal {
	p := auth.PrincipalFromContext(r.Context())
	if p == nil {
		fhirerror.WriteError(w, http.StatusUnauthorized, fhirerror.CodeLogin,
			"unauthenticated")
		return nil
	}
	return p
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func parseUUID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// readCappedBody reads up to cap+1 bytes; returns oversize=true when
// the body exceeds the cap, so the caller can answer 413 (S-2.2). cap
// of 0 falls back to DefaultMaxBodyBytes.
func readCappedBody(r *http.Request, cap int64) (body []byte, err error, oversize bool) {
	if cap <= 0 {
		cap = DefaultMaxBodyBytes
	}
	body, err = io.ReadAll(io.LimitReader(r.Body, cap+1))
	if err != nil {
		return nil, err, false
	}
	if int64(len(body)) > cap {
		return nil, nil, true
	}
	return body, nil, false
}

// logActivateError emits a warn-level log when a database / audit /
// channel call inside the fire-and-forget activation goroutine fails
// (S-2.7). Nil logger is a no-op so existing tests / callers that
// don't wire a logger keep working.
func (s *server) logActivateError(op string, id uuid.UUID, err error) {
	if s.deps.Logger == nil || err == nil {
		return
	}
	s.deps.Logger.Warn(op, "subscription_id", id.String(), "err", err.Error())
}

// parseEventNumberParam parses an optional event-number query
// parameter. Empty means 0 (unbounded). Returns ok=false on a
// malformed or negative value so the handler can answer 400 instead
// of silently treating it as unbounded (S-2.10).
func parseEventNumberParam(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// capDiagnostic shortens diagnostics text so a pathological JSON body
// can't push a multi-megabyte error message into the FHIR
// OperationOutcome (S-2.3). cap of 0 means DefaultMaxSchemaErrorBytes.
func capDiagnostic(s string, cap int) string {
	if cap <= 0 {
		cap = DefaultMaxSchemaErrorBytes
	}
	if len(s) <= cap {
		return s
	}
	const suffix = "... (truncated)"
	if cap <= len(suffix) {
		return s[:cap]
	}
	return s[:cap-len(suffix)] + suffix
}

// createSubscription is POST /Subscription.
func (s *server) createSubscription(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.c") {
		return
	}
	body, readErr, oversize := readCappedBody(r, s.deps.MaxBodyBytes)
	if oversize {
		fhirerror.WriteError(w, http.StatusRequestEntityTooLarge, fhirerror.CodeValue,
			"request body exceeds limit")
		return
	}
	if readErr != nil {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure,
			"could not read body")
		return
	}
	if vErr := schemas.ValidateSubscription(body); vErr != nil {
		s.recordValidationFailure("schema")
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure,
			capDiagnostic(vErr.Error(), s.deps.MaxSchemaErrorBytes))
		return
	}
	internal, parseErr := parseInternalFromBody(body)
	if parseErr != nil {
		s.recordValidationFailure("schema")
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure,
			parseErr.Error())
		return
	}
	if internal.TopicURL == "" {
		s.recordValidationFailure("semantic")
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"topic is required")
		return
	}
	if internal.ChannelType == "" {
		s.recordValidationFailure("semantic")
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"channelType is required")
		return
	}

	// If-None-Exist (LLD §4.1): client-supplied search criteria. If any
	// existing subscription owned by this client matches the search,
	// return 412 Precondition Failed with an OperationOutcome that
	// surfaces the duplicate.
	if cond := r.Header.Get("If-None-Exist"); cond != "" {
		matches, mErr := s.matchingSubscriptions(r.Context(), p.ClientID, cond)
		if mErr != nil {
			fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
				"if-none-exist evaluation failed")
			return
		}
		if len(matches) > 0 {
			fhirerror.WriteError(w, http.StatusPreconditionFailed, fhirerror.CodeConflict,
				"a matching Subscription already exists")
			return
		}
	}

	// Topic must be active in the catalog.
	topic, err := s.findActiveTopicByURL(r.Context(), internal.TopicURL)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"topic catalog lookup failed")
		return
	}
	if topic == nil {
		s.recordValidationFailure("semantic")
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"topic not in catalog")
		return
	}

	// Channel must be registered in this deployment.
	if _, ok := s.deps.Channels[internal.ChannelType]; !ok {
		s.recordValidationFailure("semantic")
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"unsupported channelType")
		return
	}

	// SSRF guard (B-11). The validator runs on every channel that
	// carries an outbound URL. websocket has no caller-supplied URL so
	// we skip the check there. The same validator is reused at delivery
	// time by the rest-hook channel as a DNS-rebinding defense.
	if s.deps.URLValidator != nil && internal.Endpoint != "" && internal.ChannelType != "websocket" {
		if err := s.deps.URLValidator.Validate(internal.Endpoint); err != nil {
			s.recordValidationFailure("ssrf")
			fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeValue,
				"endpoint rejected by SSRF policy")
			return
		}
	}

	row := internal.toRow(p.ClientID, repos.SubRequested)
	id, insertErr := s.deps.Subscriptions.Insert(r.Context(), row)
	if insertErr != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"insert failed")
		return
	}
	row.ID = id
	auditBody, _ := RedactSubscriptionForAudit(body, AuditRedactConfig{MaxBytes: s.deps.AuditMaxBytes})
	_ = s.deps.Audit.Append(r.Context(), "subscription.create", id.String(), "success", nil, auditBody)
	s.recordCreated()

	// Activation handshake — non-blocking. The 201 returns immediately
	// with status=requested. spawnActivate enrolls the goroutine in
	// the lifecycle WaitGroup, bounds the per-call context with
	// ActivationTimeout, and recovers any panic in the channel adapter
	// (B-10).
	s.spawnActivate(id)

	stored, _ := s.deps.Subscriptions.GetByID(r.Context(), id)
	if stored == nil {
		stored = &row
	}

	resp := renderSubscription(stored)
	w.Header().Set("Location", "/Subscription/"+id.String())
	w.Header().Set("ETag", `W/"`+id.String()+`"`)
	writeJSON(w, http.StatusCreated, resp)
}

// activate runs the channel's on_subscription_activated handshake and
// flips the subscription status accordingly. DB / channel / audit
// errors are routed to the dep-injected logger so an operator can spot
// silent activation failures (S-2.7); previously every error was
// dropped to `_`.
func (s *server) activate(ctx context.Context, id uuid.UUID) {
	sub, err := s.deps.Subscriptions.GetByID(ctx, id)
	if err != nil {
		s.logActivateError("activate.get_subscription", id, err)
		return
	}
	if sub == nil {
		return
	}
	channel, ok := s.deps.Channels[sub.ChannelType]
	if !ok {
		if err := s.deps.Subscriptions.UpdateStatus(ctx, id, repos.SubError, "no channel registered"); err != nil {
			s.logActivateError("activate.no_channel.update_status", id, err)
		}
		return
	}
	outcome, err := channel.ActivateSubscription(ctx, *sub)
	if err != nil || outcome == HandshakeFailed {
		reason := "handshake failed"
		if err != nil {
			reason = err.Error()
		}
		// If the per-call ctx is dead (timeout / lifecycle cancel),
		// fall back to a fresh background ctx so the row does not stay
		// stuck at `requested` (B-10).
		bookkeepingCtx := ctx
		if bookkeepingCtx.Err() != nil {
			bookkeepingCtx = context.Background()
		}
		if uErr := s.deps.Subscriptions.UpdateStatus(bookkeepingCtx, id, repos.SubError, reason); uErr != nil {
			s.logActivateError("activate.handshake_fail.update_status", id, uErr)
		}
		if aErr := s.deps.Audit.Append(bookkeepingCtx, "subscription.handshake.fail", id.String(), "failure", nil, nil); aErr != nil {
			s.logActivateError("activate.handshake_fail.audit", id, aErr)
		}
		return
	}
	if err := s.deps.Subscriptions.UpdateStatus(ctx, id, repos.SubActive, ""); err != nil {
		s.logActivateError("activate.success.update_status", id, err)
	}
	if err := s.deps.Audit.Append(ctx, "subscription.handshake.ok", id.String(), "success", nil, nil); err != nil {
		s.logActivateError("activate.success.audit", id, err)
	}
}

// matchingSubscriptions returns this client's subscriptions whose
// topic / channelType / endpoint matches the search criteria in the
// `If-None-Exist` header. The criteria are FHIR-search query string
// fragments like `topic=http://... &channelType=rest-hook`. This is
// intentionally narrow — only the LLD §4.1 fields participate.
func (s *server) matchingSubscriptions(ctx context.Context, clientID, criteria string) ([]repos.SubscriptionRow, error) {
	rows, err := s.deps.Subscriptions.ListByClient(ctx, clientID)
	if err != nil {
		return nil, err
	}
	q, parseErr := url.ParseQuery(criteria)
	if parseErr != nil {
		return nil, parseErr
	}
	var out []repos.SubscriptionRow
	for i := range rows {
		row := &rows[i]
		if v := q.Get("topic"); v != "" && row.TopicURL != v {
			continue
		}
		if v := q.Get("channelType"); v != "" && row.ChannelType != v {
			continue
		}
		if v := q.Get("endpoint"); v != "" && row.Endpoint != v {
			continue
		}
		// off subscriptions are tombstones — they don't block creates.
		if row.Status == repos.SubOff {
			continue
		}
		out = append(out, *row)
	}
	return out, nil
}

// findActiveTopicByURL returns the active topic with the highest
// version (lexicographic) for a given URL, or nil if none.
func (s *server) findActiveTopicByURL(ctx context.Context, url string) (*repos.SubscriptionTopicRow, error) {
	rows, err := s.deps.Topics.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	var best *repos.SubscriptionTopicRow
	for i := range rows {
		row := &rows[i]
		if row.URL != url {
			continue
		}
		if best == nil || row.Version > best.Version {
			best = row
		}
	}
	return best, nil
}

// readSubscription is GET /Subscription/{id}.
func (s *server) readSubscription(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.r") {
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	row, err := s.deps.Subscriptions.GetByID(r.Context(), id)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"read failed")
		return
	}
	if row == nil || row.ClientID != p.ClientID {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	w.Header().Set("ETag", `W/"`+row.ID.String()+`"`)
	writeJSON(w, http.StatusOK, renderSubscription(row))
}

// searchSubscriptions is GET /Subscription. Returns a searchset Bundle
// with the principal's owned subscriptions.
func (s *server) searchSubscriptions(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.r") {
		return
	}
	rows, err := s.deps.Subscriptions.ListByClient(r.Context(), p.ClientID)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"search failed")
		return
	}
	entries := make([]any, 0, len(rows))
	for i := range rows {
		var sub map[string]any
		_ = json.Unmarshal(renderSubscription(&rows[i]), &sub)
		entries = append(entries, map[string]any{
			"resource": sub,
		})
	}
	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "searchset",
		"total":        len(rows),
		"entry":        entries,
	}
	body, _ := json.Marshal(bundle)
	writeJSON(w, http.StatusOK, body)
}

// updateSubscription is PUT /Subscription/{id}.
func (s *server) updateSubscription(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.u") {
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	existing, err := s.deps.Subscriptions.GetByID(r.Context(), id)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"read failed")
		return
	}
	if existing == nil || existing.ClientID != p.ClientID {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}

	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
		// FHIR optimistic-concurrency requires the weak ETag form
		// (`W/"<version>"`); a bare UUID is no longer accepted because
		// then the version is the resource id and lost-update detection
		// is impossible (S-2.6).
		expected := `W/"` + existing.ID.String() + `"`
		if ifMatch != expected {
			fhirerror.WriteError(w, http.StatusConflict, fhirerror.CodeConflict,
				"version mismatch")
			return
		}
	}

	body, readErr, oversize := readCappedBody(r, s.deps.MaxBodyBytes)
	if oversize {
		fhirerror.WriteError(w, http.StatusRequestEntityTooLarge, fhirerror.CodeValue,
			"request body exceeds limit")
		return
	}
	if readErr != nil {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure, "could not read body")
		return
	}
	if vErr := schemas.ValidateSubscription(body); vErr != nil {
		s.recordValidationFailure("schema")
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure, capDiagnostic(vErr.Error(), s.deps.MaxSchemaErrorBytes))
		return
	}
	newDoc, err := parseInternalFromBody(body)
	if err != nil {
		s.recordValidationFailure("schema")
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure, err.Error())
		return
	}
	if newDoc.TopicURL == "" {
		s.recordValidationFailure("semantic")
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"topic required")
		return
	}
	topic, err := s.findActiveTopicByURL(r.Context(), newDoc.TopicURL)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException, "topic lookup failed")
		return
	}
	if topic == nil {
		s.recordValidationFailure("semantic")
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"topic not in catalog")
		return
	}
	if _, ok := s.deps.Channels[newDoc.ChannelType]; !ok {
		s.recordValidationFailure("semantic")
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"unsupported channelType")
		return
	}

	if s.deps.URLValidator != nil && newDoc.Endpoint != "" && newDoc.ChannelType != "websocket" {
		if err := s.deps.URLValidator.Validate(newDoc.Endpoint); err != nil {
			s.recordValidationFailure("ssrf")
			fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeValue,
				"endpoint rejected by SSRF policy")
			return
		}
	}

	classification := classifyUpdate(existing, newDoc)
	updatedRow := newDoc.toRow(p.ClientID, existing.Status)
	updatedRow.EventsSinceSubscriptionStart = existing.EventsSinceSubscriptionStart

	if err := s.deps.Subscriptions.UpdateResource(r.Context(), id, updatedRow); err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"update failed")
		return
	}
	auditBody, _ := RedactSubscriptionForAudit(body, AuditRedactConfig{MaxBytes: s.deps.AuditMaxBytes})
	_ = s.deps.Audit.Append(r.Context(), "subscription.update", id.String(), "success", nil, auditBody)
	s.recordUpdated()

	switch classification {
	case routingReHandshake:
		_ = s.deps.Subscriptions.UpdateStatus(r.Context(), id, repos.SubRequested, "")
		s.spawnActivate(id)
	case routingDeactivate:
		_ = s.deps.Subscriptions.UpdateStatus(r.Context(), id, repos.SubOff, "")
	case routingDrainAndApply:
		// Engine handles the drain; status not changed by the API. The
		// engine is signaled out-of-band; for v1 the row update is
		// sufficient and the engine picks up the change on next read.
	}

	row, _ := s.deps.Subscriptions.GetByID(r.Context(), id)
	if row == nil {
		row = &updatedRow
		row.ID = id
	}
	w.Header().Set("ETag", `W/"`+id.String()+`"`)
	writeJSON(w, http.StatusOK, renderSubscription(row))
}

type updateRouting int

const (
	routingTakesEffectImmediately updateRouting = iota
	routingDrainAndApply
	routingReHandshake
	routingDeactivate
)

func classifyUpdate(existing *repos.SubscriptionRow, want *internalSubscription) updateRouting {
	if want.Status == "off" {
		return routingDeactivate
	}
	if existing.Endpoint != want.Endpoint || existing.ChannelType != want.ChannelType ||
		!equalJSON(existing.Header, want.Header) {
		return routingReHandshake
	}
	if existing.TopicURL != want.TopicURL || !equalJSON(existing.FilterBy, want.FilterBy) {
		return routingDrainAndApply
	}
	return routingTakesEffectImmediately
}

func equalJSON(a, b []byte) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	var av, bv any
	_ = json.Unmarshal(a, &av)
	_ = json.Unmarshal(b, &bv)
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return bytes.Equal(ab, bb)
}

// deleteSubscription is DELETE /Subscription/{id}.
func (s *server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.d") {
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	existing, err := s.deps.Subscriptions.GetByID(r.Context(), id)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException, "read failed")
		return
	}
	if existing == nil || existing.ClientID != p.ClientID {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	if err := s.deps.Subscriptions.UpdateStatus(r.Context(), id, repos.SubOff, ""); err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException, "delete failed")
		return
	}
	_ = s.deps.Audit.Append(r.Context(), "subscription.delete", id.String(), "success", nil, nil)
	s.recordDeleted()
	w.WriteHeader(http.StatusNoContent)
}

// opStatusSingle is GET /Subscription/{id}/$status.
func (s *server) opStatusSingle(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.r") {
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	sub, err := s.deps.Subscriptions.GetByID(r.Context(), id)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException, "read failed")
		return
	}
	if sub == nil || sub.ClientID != p.ClientID {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	statusResource := s.buildSubscriptionStatus(r.Context(), sub, "query-status", nil)
	bundle := wrapSearchset([]any{statusResource})
	body, _ := json.Marshal(bundle)
	writeJSON(w, http.StatusOK, body)
}

// opStatusBulk is GET /Subscription/$status with ?id=... query params.
func (s *server) opStatusBulk(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.r") {
		return
	}
	ids := r.URL.Query()["id"]
	if len(ids) == 0 {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeValue,
			"id parameter required")
		return
	}
	// Cap fan-out so an attacker cannot pin the DB pool with one
	// request (S-2.11).
	if len(ids) > s.deps.MaxStatusBulkIDs {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeValue,
			"too many id parameters")
		return
	}
	entries := make([]any, 0, len(ids))
	for _, raw := range ids {
		id, ok := parseUUID(raw)
		if !ok {
			entries = append(entries, makeOutcomeEntry(raw, "not-found"))
			continue
		}
		sub, err := s.deps.Subscriptions.GetByID(r.Context(), id)
		if err != nil || sub == nil || sub.ClientID != p.ClientID {
			entries = append(entries, makeOutcomeEntry(raw, "not-found"))
			continue
		}
		entries = append(entries, s.buildSubscriptionStatus(r.Context(), sub, "query-status", nil))
	}
	bundle := wrapSearchset(entries)
	body, _ := json.Marshal(bundle)
	writeJSON(w, http.StatusOK, body)
}

// opEvents is GET /Subscription/{id}/$events.
func (s *server) opEvents(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.r") {
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	sub, err := s.deps.Subscriptions.GetByID(r.Context(), id)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException, "read failed")
		return
	}
	if sub == nil || sub.ClientID != p.ClientID {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}

	since, ok := parseEventNumberParam(r.URL.Query().Get("eventsSinceNumber"))
	if !ok {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeValue,
			"eventsSinceNumber must be a non-negative integer")
		return
	}
	until, ok := parseEventNumberParam(r.URL.Query().Get("eventsUntilNumber"))
	if !ok {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeValue,
			"eventsUntilNumber must be a non-negative integer")
		return
	}

	events, err := s.deps.Events.ListByTopicAndRange(r.Context(), sub.TopicURL, since, until)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"event log read failed")
		return
	}

	notificationEvents := make([]map[string]any, 0, len(events))
	for i := range events {
		ev := &events[i]
		entry := map[string]any{
			"eventNumber": ev.EventNumber,
			"timestamp":   ev.OccurredAt.UTC().Format(instantFormat),
			"focus":       map[string]any{"reference": ev.Focus},
		}
		notificationEvents = append(notificationEvents, entry)
	}

	statusResource := s.buildSubscriptionStatus(r.Context(), sub, "query-event", notificationEvents)
	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "subscription-notification",
		"timestamp":    s.deps.Now().UTC().Format(instantFormat),
		"entry": []any{
			map[string]any{"resource": statusResource},
		},
	}
	body, _ := json.Marshal(bundle)
	writeJSON(w, http.StatusOK, body)
}

// opGetWsBindingToken is POST /Subscription/{id}/$get-ws-binding-token.
func (s *server) opGetWsBindingToken(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.r") {
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	sub, err := s.deps.Subscriptions.GetByID(r.Context(), id)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException, "read failed")
		return
	}
	if sub == nil || sub.ClientID != p.ClientID {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound,
			"no such subscription")
		return
	}
	if sub.ChannelType != "websocket" {
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"subscription is not websocket")
		return
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException, "rand failed")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expires := s.deps.Now().Add(s.deps.WSBindingTTL)

	if err := s.deps.WsTokens.Insert(r.Context(), repos.WsBindingTokenRow{
		Token:          token,
		SubscriptionID: id,
		ClientID:       p.ClientID,
		ExpiresAt:      expires,
	}); err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"token persistence failed")
		return
	}
	_ = s.deps.Audit.Append(r.Context(), "subscription.ws-binding-token.issue", id.String(), "success", nil, nil)
	s.recordWsTokenIssued()

	wsURL := s.deps.WSBaseURL
	resp := map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "token", "valueString": token},
			map[string]any{"name": "expiration", "valueDateTime": expires.UTC().Format(instantFormat)},
			map[string]any{"name": "subscription", "valueReference": map[string]any{"reference": "Subscription/" + id.String()}},
			map[string]any{"name": "websocket-url", "valueUrl": wsURL},
		},
	}
	body, _ := json.Marshal(resp)
	writeJSON(w, http.StatusOK, body)
}

// searchTopics is GET /SubscriptionTopic.
func (s *server) searchTopics(w http.ResponseWriter, r *http.Request) {
	if mustPrincipal(w, r) == nil {
		return
	}
	rows, err := s.deps.Topics.ListActive(r.Context())
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"topic catalog lookup failed")
		return
	}
	entries := make([]any, 0, len(rows))
	for i := range rows {
		var topic map[string]any
		if uErr := json.Unmarshal(rows[i].Body, &topic); uErr == nil {
			entries = append(entries, map[string]any{"resource": topic})
		}
	}
	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "searchset",
		"total":        len(entries),
		"entry":        entries,
	}
	body, _ := json.Marshal(bundle)
	writeJSON(w, http.StatusOK, body)
}

// readTopic is GET /SubscriptionTopic/{id}.
func (s *server) readTopic(w http.ResponseWriter, r *http.Request) {
	if mustPrincipal(w, r) == nil {
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound, "no such topic")
		return
	}
	rows, err := s.deps.Topics.ListActive(r.Context())
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"topic catalog lookup failed")
		return
	}
	for i := range rows {
		if rows[i].ID != id {
			continue
		}
		var topic map[string]any
		_ = json.Unmarshal(rows[i].Body, &topic)
		body, _ := json.Marshal(topic)
		writeJSON(w, http.StatusOK, body)
		return
	}
	fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound, "no such topic")
}

// getCapabilityStatement is GET /metadata when mounted behind the
// authenticated surface (RegisterRoutes).
func (s *server) getCapabilityStatement(w http.ResponseWriter, r *http.Request) {
	if mustPrincipal(w, r) == nil {
		return
	}
	s.writeCapabilityStatement(w, r)
}

// getCapabilityStatementPublic serves /metadata on the pre-auth
// surface (RegisterPublicRoutes). FHIR conformance probes hit
// /metadata without a bearer token (S-2.1).
func (s *server) getCapabilityStatementPublic(w http.ResponseWriter, r *http.Request) {
	s.writeCapabilityStatement(w, r)
}

func (s *server) writeCapabilityStatement(w http.ResponseWriter, r *http.Request) {
	cs := s.buildCapabilityStatement(r.Context())
	body, _ := json.Marshal(cs)
	writeJSON(w, http.StatusOK, body)
}

// buildSubscriptionStatus assembles a SubscriptionStatus resource per
// the spec. ctx is the caller's request context so deadline / cancel
// propagate to the deliveries lookup (S-2.12).
func (s *server) buildSubscriptionStatus(ctx context.Context, sub *repos.SubscriptionRow, kind string, events []map[string]any) map[string]any {
	if ctx == nil {
		ctx = context.Background()
	}
	last, _ := s.deps.Deliveries.LastDeliveredEventNumber(ctx, sub.ID)
	out := map[string]any{
		"resourceType":                 "SubscriptionStatus",
		"status":                       string(sub.Status),
		"type":                         kind,
		"eventsSinceSubscriptionStart": sub.EventsSinceSubscriptionStart,
		"subscription":                 map[string]any{"reference": "Subscription/" + sub.ID.String()},
		"topic":                        sub.TopicURL,
	}
	if last > 0 {
		out["lastDeliveredEventNumber"] = last
	}
	if sub.Error != "" {
		out["error"] = []any{map[string]any{"text": sub.Error}}
	}
	if events != nil {
		out["notificationEvent"] = events
	}
	return out
}

func wrapSearchset(entries []any) map[string]any {
	wrapped := make([]any, 0, len(entries))
	for _, e := range entries {
		wrapped = append(wrapped, map[string]any{"resource": e})
	}
	return map[string]any{
		"resourceType": "Bundle",
		"type":         "searchset",
		"total":        len(wrapped),
		"entry":        wrapped,
	}
}

func makeOutcomeEntry(refID, code string) map[string]any {
	return map[string]any{
		"resource": map[string]any{
			"resourceType": "OperationOutcome",
			"issue": []any{
				map[string]any{
					"severity":    "error",
					"code":        code,
					"diagnostics": "no such subscription: " + refID,
				},
			},
		},
	}
}

func (s *server) buildCapabilityStatement(ctx context.Context) map[string]any {
	rows, _ := s.deps.Topics.ListActive(ctx)
	channelCodes := make([]any, 0, len(s.deps.Channels))
	for code := range s.deps.Channels {
		channelCodes = append(channelCodes, map[string]any{"code": code})
	}
	topicURLs := make([]any, 0, len(rows))
	for i := range rows {
		topicURLs = append(topicURLs, map[string]any{"url": rows[i].URL, "version": rows[i].Version})
	}
	return map[string]any{
		"resourceType": "CapabilityStatement",
		"status":       "active",
		"date":         s.deps.Now().UTC().Format(instantFormat),
		"kind":         "instance",
		"software": map[string]any{
			"name":    "fhir-ehr-subscriptions-service",
			"version": s.deps.ServerVersion,
		},
		"implementation": map[string]any{
			"description": "FHIR Subscriptions Bridge",
			"url":         s.deps.BaseURL,
		},
		"fhirVersion": s.deps.FHIRVersion,
		"format":      []any{"application/fhir+json"},
		"rest": []any{
			map[string]any{
				"mode": "server",
				"resource": []any{
					map[string]any{
						"type":        "Subscription",
						"interaction": []any{map[string]any{"code": "create"}, map[string]any{"code": "read"}, map[string]any{"code": "update"}, map[string]any{"code": "delete"}, map[string]any{"code": "search-type"}},
						"operation": []any{
							map[string]any{"name": "status", "definition": "http://hl7.org/fhir/OperationDefinition/Subscription-status"},
							map[string]any{"name": "events", "definition": "http://hl7.org/fhir/OperationDefinition/Subscription-events"},
							map[string]any{"name": "get-ws-binding-token", "definition": "http://hl7.org/fhir/OperationDefinition/Subscription-get-ws-binding-token"},
						},
					},
					map[string]any{
						"type":        "SubscriptionTopic",
						"interaction": []any{map[string]any{"code": "read"}, map[string]any{"code": "search-type"}},
					},
				},
				"security": map[string]any{
					"service": []any{
						map[string]any{
							"coding": []any{
								map[string]any{
									"system": "http://terminology.hl7.org/CodeSystem/restful-security-service",
									"code":   "SMART-on-FHIR",
								},
							},
						},
					},
				},
			},
		},
		"extension": []any{
			map[string]any{"url": "supported-channels", "valueCode": "rest-hook,websocket"},
			map[string]any{"url": "supported-topics", "extension": topicURLs},
			map[string]any{"url": "supported-channel-set", "extension": channelCodes},
		},
	}
}
