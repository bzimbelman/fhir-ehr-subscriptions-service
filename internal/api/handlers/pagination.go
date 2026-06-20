// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// encodeSubscriptionCursor produces an opaque cursor string from a
// (CreatedAt, ID) pair. The encoding is a base64url(no-pad) of
// "<unix-nanos>:<uuid>" so it is short, URL-safe, and trivially
// inspectable in logs without exposing internal indexes.
func encodeSubscriptionCursor(c SubscriptionCursor) string {
	raw := fmt.Sprintf("%d:%s", c.CreatedAt.UTC().UnixNano(), c.ID.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeSubscriptionCursor reverses encodeSubscriptionCursor. Returns
// false on any malformation so the caller can answer 400.
func decodeSubscriptionCursor(s string) (SubscriptionCursor, bool) {
	if s == "" {
		return SubscriptionCursor{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return SubscriptionCursor{}, false
	}
	idx := strings.IndexByte(string(raw), ':')
	if idx <= 0 || idx == len(raw)-1 {
		return SubscriptionCursor{}, false
	}
	nanosPart := string(raw[:idx])
	idPart := string(raw[idx+1:])
	// strconv.ParseInt is strict — leading "+", trailing junk, or any
	// non-decimal character fails. The cursor is a server-emitted token
	// so the input shape is fully under our control; using strconv keeps
	// the parse semantics consistent with parseCountParam (OP #189) and
	// avoids fmt.Sscanf's permissive trailing-junk acceptance.
	nanos, scanErr := strconv.ParseInt(nanosPart, 10, 64)
	if scanErr != nil {
		return SubscriptionCursor{}, false
	}
	id, err := uuid.Parse(idPart)
	if err != nil {
		return SubscriptionCursor{}, false
	}
	return SubscriptionCursor{
		CreatedAt: time.Unix(0, nanos).UTC(),
		ID:        id,
	}, true
}
