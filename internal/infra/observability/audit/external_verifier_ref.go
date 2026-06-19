// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// ExternalVerifierRow is the slim shape an external auditor reads from
// the audit_log table. It mirrors the persisted columns 1:1 — no
// dependence on the internal Row type or the audit package's internal
// representation. Auditors are expected to construct this from a plain
// SELECT against audit_log; the field names match the table columns.
type ExternalVerifierRow struct {
	OccurredAt    time.Time
	ActorKind     string
	ActorID       string
	Action        string
	TargetKind    string
	TargetID      string
	Outcome       string
	CorrelationID uuid.UUID
	Payload       map[string]any
	PriorHash     []byte
	ChainHash     []byte
}

// VerifyChainExternal is the third-party reference verifier required by
// story #108 AC #3. It walks rows in insertion order and recomputes
// each row's chain_hash from the on-disk fields without touching any
// internal audit package state. It exists so an auditor can copy this
// ~50-line function into their own tooling, point it at the audit_log
// SELECT, and reproduce the chain_hash byte-for-byte.
//
// The canonical-input rules:
//
//   - JCS-canonical JSON object (sorted keys, no whitespace).
//   - Keys: ts, actor_kind, actor_id, action, target_kind, target_id,
//     outcome, correlation_id, payload, prior_hash.
//   - ts is RFC3339Nano in UTC.
//   - correlation_id is the UUID's canonical 36-char string form.
//   - prior_hash is RFC 4648 standard base64 of the raw 32-byte
//     SHA-256 digest of the prior row's chain_hash. Genesis is
//     SHA-256("fhir-ehr-subscriptions-service audit chain genesis").
//   - chain_hash = SHA-256(canonical_input_bytes).
//
// Returns the index of the first mismatch, or -1 on a clean chain.
// `breaks` is the count of every row whose recomputed chain_hash did
// not match the stored value (story #108 — does NOT silently
// re-anchor; downstream rows after a tamper surface as additional
// breaks).
func VerifyChainExternal(rows []ExternalVerifierRow, genesisLiteral string) (firstBadIdx, breaks int) {
	bad := externalVerifierBadIndices(rows, genesisLiteral)
	firstBadIdx = -1
	if len(bad) > 0 {
		firstBadIdx = bad[0]
	}
	return firstBadIdx, len(bad)
}

// VerifyChainExternalDetail is the per-row companion to VerifyChainExternal
// for tooling that wants to enumerate every mismatch (e.g. cmd/audit-
// chain-walker, OP #257 H2). Returns the zero-based indices of every
// row whose recomputed chain_hash did not match the stored value, in
// ascending order. Empty slice means a clean chain.
//
// The chain math is identical to VerifyChainExternal — the same
// canonicalization, the same prior advance, the same no-re-anchor
// guarantee — so a tooling caller never needs to re-derive the chain
// rules.
func VerifyChainExternalDetail(rows []ExternalVerifierRow, genesisLiteral string) []int {
	return externalVerifierBadIndices(rows, genesisLiteral)
}

func externalVerifierBadIndices(rows []ExternalVerifierRow, genesisLiteral string) []int {
	prior := GenesisHashFromLiteral(genesisLiteral)
	var bad []int
	for i, r := range rows {
		obj := map[string]any{
			"ts":             r.OccurredAt.UTC().Format(time.RFC3339Nano),
			"actor_kind":     r.ActorKind,
			"actor_id":       r.ActorID,
			"action":         r.Action,
			"target_kind":    r.TargetKind,
			"target_id":      r.TargetID,
			"outcome":        r.Outcome,
			"correlation_id": r.CorrelationID.String(),
			"payload":        r.Payload,
			"prior_hash":     base64.StdEncoding.EncodeToString(prior),
		}
		canon := canonicalJCSExternal(obj)
		sum := sha256.Sum256(canon)
		if !equalBytes(sum[:], r.ChainHash) {
			bad = append(bad, i)
		}
		// Advance prior with the verifier-recomputed hash, NOT
		// r.ChainHash. Re-anchoring on r.ChainHash would silently
		// glue the walker onto a corrupted row and let downstream
		// rows pass verification.
		prior = sum[:]
	}
	return bad
}

func canonicalJCSExternal(obj map[string]any) []byte {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []byte
	out = append(out, '{')
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		kb, _ := json.Marshal(k)
		out = append(out, kb...)
		out = append(out, ':')
		raw, _ := json.Marshal(obj[k])
		canon, err := CanonicalizeJSON(raw)
		if err != nil {
			out = append(out, []byte(fmt.Sprintf("%q", err.Error()))...)
			continue
		}
		out = append(out, canon...)
	}
	out = append(out, '}')
	return out
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
