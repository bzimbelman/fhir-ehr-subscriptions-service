// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/api/auth"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/api/fhirerror"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/api/schemas"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

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

// createSubscription is POST /Subscription.
func (s *server) createSubscription(w http.ResponseWriter, r *http.Request) {
	p := mustPrincipal(w, r)
	if p == nil {
		return
	}
	if !requireScopes(w, p, "system/Subscription.c") {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure,
			"could not read body")
		return
	}
	if err := schemas.ValidateSubscription(body); err != nil {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure,
			err.Error())
		return
	}
	internal, err := parseInternalFromBody(body)
	if err != nil {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure,
			err.Error())
		return
	}
	if internal.TopicURL == "" {
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"topic is required")
		return
	}
	if internal.ChannelType == "" {
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"channelType is required")
		return
	}

	// Topic must be active in the catalog.
	topic, err := s.findActiveTopicByURL(r.Context(), internal.TopicURL)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"topic catalog lookup failed")
		return
	}
	if topic == nil {
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"topic not in catalog")
		return
	}

	// Channel must be registered in this deployment.
	if _, ok := s.deps.Channels[internal.ChannelType]; !ok {
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"unsupported channelType")
		return
	}

	row := internal.toRow(p.ClientID, repos.SubRequested)
	id, err := s.deps.Subscriptions.Insert(r.Context(), row)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"insert failed")
		return
	}
	row.ID = id
	_ = s.deps.Audit.Append(r.Context(), "subscription.create", id.String(), "success", nil, body)

	// Activation handshake — non-blocking. The 201 returns immediately
	// with status=requested.
	go s.activate(context.Background(), id)

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
// flips the subscription status accordingly. Errors are recorded; the
// subscription is left in `error` on failure.
func (s *server) activate(ctx context.Context, id uuid.UUID) {
	sub, err := s.deps.Subscriptions.GetByID(ctx, id)
	if err != nil || sub == nil {
		return
	}
	channel, ok := s.deps.Channels[sub.ChannelType]
	if !ok {
		_ = s.deps.Subscriptions.UpdateStatus(ctx, id, repos.SubError, "no channel registered")
		return
	}
	outcome, err := channel.ActivateSubscription(ctx, *sub)
	if err != nil || outcome == HandshakeFailed {
		reason := "handshake failed"
		if err != nil {
			reason = err.Error()
		}
		_ = s.deps.Subscriptions.UpdateStatus(ctx, id, repos.SubError, reason)
		_ = s.deps.Audit.Append(ctx, "subscription.handshake.fail", id.String(), "failure", nil, nil)
		return
	}
	_ = s.deps.Subscriptions.UpdateStatus(ctx, id, repos.SubActive, "")
	_ = s.deps.Audit.Append(ctx, "subscription.handshake.ok", id.String(), "success", nil, nil)
}

// findActiveTopicByURL returns the active topic with the highest
// version (lexicographic) for a given URL, or nil if none.
func (s *server) findActiveTopicByURL(ctx context.Context, url string) (*repos.SubscriptionTopicRow, error) {
	rows, err := s.deps.Topics.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	var best *repos.SubscriptionTopicRow
	for i, r := range rows {
		if r.URL != url {
			continue
		}
		if best == nil || r.Version > best.Version {
			best = &rows[i]
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
		expected := `W/"` + existing.ID.String() + `"`
		if ifMatch != expected && ifMatch != existing.ID.String() {
			fhirerror.WriteError(w, http.StatusConflict, fhirerror.CodeConflict,
				"version mismatch")
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure, "could not read body")
		return
	}
	if err := schemas.ValidateSubscription(body); err != nil {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure, err.Error())
		return
	}
	newDoc, err := parseInternalFromBody(body)
	if err != nil {
		fhirerror.WriteError(w, http.StatusBadRequest, fhirerror.CodeStructure, err.Error())
		return
	}
	if newDoc.TopicURL == "" {
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
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"topic not in catalog")
		return
	}
	if _, ok := s.deps.Channels[newDoc.ChannelType]; !ok {
		fhirerror.WriteError(w, http.StatusUnprocessableEntity, fhirerror.CodeBusinessRule,
			"unsupported channelType")
		return
	}

	classification := classifyUpdate(existing, newDoc)
	updatedRow := newDoc.toRow(p.ClientID, existing.Status)
	updatedRow.EventsSinceSubscriptionStart = existing.EventsSinceSubscriptionStart

	if err := s.deps.Subscriptions.UpdateResource(r.Context(), id, updatedRow); err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"update failed")
		return
	}
	_ = s.deps.Audit.Append(r.Context(), "subscription.update", id.String(), "success", nil, body)

	switch classification {
	case routingReHandshake:
		_ = s.deps.Subscriptions.UpdateStatus(r.Context(), id, repos.SubRequested, "")
		go s.activate(context.Background(), id)
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
	return string(ab) == string(bb)
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
	statusResource := s.buildSubscriptionStatus(sub, "query-status", nil)
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
		entries = append(entries, s.buildSubscriptionStatus(sub, "query-status", nil))
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

	since, _ := strconv.ParseInt(r.URL.Query().Get("eventsSinceNumber"), 10, 64)
	until, _ := strconv.ParseInt(r.URL.Query().Get("eventsUntilNumber"), 10, 64)

	events, err := s.deps.Events.ListByTopicAndRange(r.Context(), sub.TopicURL, since, until)
	if err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"event log read failed")
		return
	}

	notificationEvents := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		entry := map[string]any{
			"eventNumber":            ev.EventNumber,
			"timestamp":              ev.OccurredAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			"focus":                  map[string]any{"reference": ev.Focus},
		}
		notificationEvents = append(notificationEvents, entry)
	}

	statusResource := s.buildSubscriptionStatus(sub, "query-event", notificationEvents)
	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "subscription-notification",
		"timestamp":    s.deps.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
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
	hashSum := sha256.Sum256([]byte(token))
	hashed := hex.EncodeToString(hashSum[:])
	expires := s.deps.Now().Add(s.deps.WSBindingTTL)

	if err := s.deps.WsTokens.Insert(r.Context(), repos.WsBindingTokenRow{
		Token:          hashed,
		SubscriptionID: id,
		ClientID:       p.ClientID,
		ExpiresAt:      expires,
	}); err != nil {
		fhirerror.WriteError(w, http.StatusInternalServerError, fhirerror.CodeException,
			"token persistence failed")
		return
	}
	_ = s.deps.Audit.Append(r.Context(), "subscription.ws-binding-token.issue", id.String(), "success", nil, nil)

	wsURL := s.deps.WSBaseURL
	resp := map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "token", "valueString": token},
			map[string]any{"name": "expiration", "valueDateTime": expires.UTC().Format("2006-01-02T15:04:05Z07:00")},
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
	for _, row := range rows {
		var topic map[string]any
		if err := json.Unmarshal(row.Body, &topic); err == nil {
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
	for _, row := range rows {
		if row.ID == id {
			var topic map[string]any
			_ = json.Unmarshal(row.Body, &topic)
			body, _ := json.Marshal(topic)
			writeJSON(w, http.StatusOK, body)
			return
		}
	}
	fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound, "no such topic")
}

// getCapabilityStatement is GET /metadata.
func (s *server) getCapabilityStatement(w http.ResponseWriter, r *http.Request) {
	if mustPrincipal(w, r) == nil {
		return
	}
	cs := s.buildCapabilityStatement(r.Context())
	body, _ := json.Marshal(cs)
	writeJSON(w, http.StatusOK, body)
}

// buildSubscriptionStatus assembles a SubscriptionStatus resource per
// the spec.
func (s *server) buildSubscriptionStatus(sub *repos.SubscriptionRow, kind string, events []map[string]any) map[string]any {
	last, _ := s.deps.Deliveries.LastDeliveredEventNumber(context.Background(), sub.ID)
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
	for _, t := range rows {
		topicURLs = append(topicURLs, map[string]any{"url": t.URL, "version": t.Version})
	}
	return map[string]any{
		"resourceType": "CapabilityStatement",
		"status":       "active",
		"date":         s.deps.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
		"kind":         "instance",
		"software": map[string]any{
			"name":    "fhir-subscriptions-foss",
			"version": s.deps.ServerVersion,
		},
		"implementation": map[string]any{
			"description": "FHIR Subscriptions Bridge",
			"url":         s.deps.BaseURL,
		},
		"fhirVersion": "5.0.0",
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
