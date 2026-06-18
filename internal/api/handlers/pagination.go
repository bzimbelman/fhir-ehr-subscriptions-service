// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/base64"
	"fmt"
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
	var nanos int64
	if _, scanErr := fmt.Sscanf(nanosPart, "%d", &nanos); scanErr != nil {
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
