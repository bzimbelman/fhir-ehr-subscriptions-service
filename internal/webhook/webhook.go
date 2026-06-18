// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package webhook is the vendor-push ingress (P2.9). It exposes a
// minimal HTTP receiver that vendors POST to with HMAC-signed change
// events; the handler validates the signature, parses the body into
// the canonical ResourceChange shape, and persists a row through the
// repos.ResourceChangesRepo so the downstream pipeline picks it up.
//
// MVP scope:
//   - Single signature scheme: HMAC-SHA256 over the raw request body,
//     compared against the X-Hub-Signature-256 header (the convention
//     GitHub, Stripe, and Cerner Code share). The header is "sha256=<hex>".
//     A constant-time compare prevents signature-oracle timing attacks.
//   - Per-adapter shared-secret resolution. Operators configure secrets
//     via the standard config interpolation (env / file).
//   - JSON body shape: { "resourceType": "...", "id": "...",
//     "changeKind": "create|update|delete", "resource": <FHIR resource> }.
//   - Inserts into resource_changes with the operator-supplied adapter_id;
//     the matcher worker picks it up on next tick.
//
// Out of scope (post-MVP):
//   - mTLS-only authentication
//   - Vendor-specific shape adapters (translate inbound webhook → FHIR
//     resource; today we require the vendor to push a FHIR-shaped body)
//   - Per-vendor secret rotation with overlapping windows
//   - Backpressure / dead-letter on persistent insert failure
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// SignatureHeader is the header name the receiver looks for. Value
// shape is "sha256=<hex>".
const SignatureHeader = "X-Hub-Signature-256"

// MaxBodyBytes caps the inbound body size. Vendors that need to push
// larger payloads should batch — a multi-megabyte single FHIR resource
// is a smell.
const MaxBodyBytes = 1 << 20 // 1 MiB

// SecretResolver returns the HMAC shared secret for the named adapter.
// The wiring layer adapts the operator config (config.adapters[adapterID].
// webhook_secret) to this interface.
type SecretResolver interface {
	WebhookSecret(adapterID string) (string, bool)
}

// SecretMap is a static SecretResolver backed by an in-memory map.
// Useful for tests and for deployments that load all secrets at startup.
type SecretMap map[string]string

// WebhookSecret satisfies SecretResolver.
func (m SecretMap) WebhookSecret(adapterID string) (string, bool) {
	s, ok := m[adapterID]
	if !ok {
		return "", false
	}
	return s, true
}

// NewHandler constructs a chi-mountable receiver. It installs a
// single route: POST /webhooks/{adapter}. Operators mount this on
// the public HTTP server via:
//
//	r.Mount("/webhooks", webhook.NewHandler(deps))
//
// Authentication is HMAC-only; place the receiver behind the same TLS
// boundary that fronts /metadata. Per-vendor mTLS is post-MVP.
type Handler struct {
	deps Deps
}

// Deps is the handler's dependency surface.
type Deps struct {
	// Resolver returns the HMAC secret for an adapter ID.
	Resolver SecretResolver
	// Repo persists the parsed ResourceChange.
	Repo *repos.ResourceChangesRepo
	// Querier hands the repo a *pgxpool.Pool (or any pgx.Tx) so the
	// insert runs against a real connection. Wiring layer threads
	// the production pool here.
	Querier repos.Querier
	// Clock is the time source; nil = time.Now.
	Clock func() time.Time
	// MaxClockSkew bounds how far an X-Webhook-Timestamp header may
	// drift from the server clock. Zero disables the timestamp check.
	// Recommended: 5 * time.Minute.
	MaxClockSkew time.Duration
}

// NewHandler constructs a Handler.
func NewHandler(deps Deps) *Handler {
	return &Handler{deps: deps}
}

// Mount mounts the handler under the given chi router at "/{adapter}".
// Caller mounts the router at the URL prefix it wants (typically
// "/webhooks").
func (h *Handler) Mount(r chi.Router) {
	r.Post("/{adapter}", h.handlePost)
}

// inboundEvent is the JSON body shape vendors send. Free-form FHIR
// resources ride along under `resource`. The wrapper carries the
// resource_changes columns the matcher needs.
type inboundEvent struct {
	ResourceType string          `json:"resourceType"`
	ID           string          `json:"id"`
	ChangeKind   string          `json:"changeKind"`
	OccurredAt   string          `json:"occurredAt,omitempty"`
	EventCode    string          `json:"eventCode,omitempty"`
	Resource     json.RawMessage `json:"resource"`
}

// handlePost is the HTTP entry point. Returns 202 Accepted on a
// successful enqueue; 401 on signature mismatch; 400 on a malformed
// body; 404 on an unknown adapter; 500 on a storage failure.
func (h *Handler) handlePost(w http.ResponseWriter, r *http.Request) {
	adapter := chi.URLParam(r, "adapter")
	if adapter == "" {
		http.Error(w, "missing adapter", http.StatusNotFound)
		return
	}
	secret, ok := h.deps.Resolver.WebhookSecret(adapter)
	if !ok {
		http.Error(w, "unknown adapter", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		http.Error(w, "body read error", http.StatusBadRequest)
		return
	}

	if err := verifySignature(r.Header.Get(SignatureHeader), secret, body); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	if h.deps.MaxClockSkew > 0 {
		if err := verifyTimestamp(r.Header.Get("X-Webhook-Timestamp"), h.now(), h.deps.MaxClockSkew); err != nil {
			http.Error(w, "stale timestamp", http.StatusUnauthorized)
			return
		}
	}

	var evt inboundEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		http.Error(w, "invalid body json", http.StatusBadRequest)
		return
	}
	if evt.ResourceType == "" || evt.ChangeKind == "" {
		http.Error(w, "missing resourceType or changeKind", http.StatusBadRequest)
		return
	}

	occurred := h.now()
	if evt.OccurredAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, evt.OccurredAt); err == nil {
			occurred = t
		}
	}

	row := repos.ResourceChangeRow{
		AdapterID:     adapter,
		CorrelationID: uuid.New(),
		ResourceType:  evt.ResourceType,
		ChangeKind:    repos.ChangeKind(evt.ChangeKind),
		Resource:      evt.Resource,
		OccurredAt:    occurred,
		EventCode:     evt.EventCode,
		CreatedMonth:  occurred.UTC().Truncate(24 * time.Hour),
	}
	if h.deps.Repo == nil || h.deps.Querier == nil {
		http.Error(w, "ingress not wired", http.StatusServiceUnavailable)
		return
	}
	if _, _, err := h.deps.Repo.Insert(r.Context(), h.deps.Querier, row); err != nil {
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))
}

func (h *Handler) now() time.Time {
	if h.deps.Clock != nil {
		return h.deps.Clock()
	}
	return time.Now()
}

// verifySignature does a constant-time compare of the request body's
// HMAC against the supplied header value. The header form is
// "sha256=<hex>"; anything else is rejected.
func verifySignature(header, secret string, body []byte) error {
	if header == "" {
		return errors.New("missing signature header")
	}
	if !strings.HasPrefix(header, "sha256=") {
		return errors.New("unsupported signature scheme")
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return fmt.Errorf("malformed signature hex: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	got := mac.Sum(nil)
	if !hmac.Equal(want, got) {
		return errors.New("signature mismatch")
	}
	return nil
}

// verifyTimestamp rejects timestamps that drift outside [-skew, +skew]
// from the server clock. The header value is RFC3339; missing header
// fails closed.
func verifyTimestamp(header string, now time.Time, skew time.Duration) error {
	if header == "" {
		return errors.New("missing timestamp header")
	}
	t, err := time.Parse(time.RFC3339, header)
	if err != nil {
		return fmt.Errorf("malformed timestamp: %w", err)
	}
	delta := now.Sub(t)
	if delta < 0 {
		delta = -delta
	}
	if delta > skew {
		return errors.New("timestamp outside skew window")
	}
	return nil
}
