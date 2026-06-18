// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// internalSubscription is the simplified R5-shaped subscription used
// inside the API. Fields the engine cares about are explicit; anything
// the wire form carries that we do not interpret rides along in
// rawHeader/rawFilter/rawContact JSON.
type internalSubscription struct {
	ID              string
	Status          string
	TopicURL        string
	ChannelType     string
	Endpoint        string
	Header          json.RawMessage
	FilterBy        json.RawMessage
	Contact         json.RawMessage
	Content         string
	HeartbeatPeriod *time.Duration
	Timeout         *time.Duration
	MaxCount        int32
	Reason          string
	EndTime         *time.Time
}

// parseInternalFromBody pulls fields off a JSON body that has already
// passed schema validation. The body may follow either the R5 native
// shape (top-level `topic`, `channelType`, `endpoint`, `content`) or
// the R4B Backport shape (top-level `criteria`, `channel.{type,
// endpoint, payload}`).
func parseInternalFromBody(body []byte) (*internalSubscription, error) {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("handlers: parse subscription: %w", err)
	}
	out := &internalSubscription{
		MaxCount: 1,
		Content:  "id-only",
	}
	if v, ok := doc["id"].(string); ok {
		out.ID = v
	}
	if v, ok := doc["status"].(string); ok {
		out.Status = v
	}
	if v, ok := doc["reason"].(string); ok {
		out.Reason = v
	}

	// Topic: prefer `topic` (R5), fall back to `criteria` (R4B).
	if v, ok := doc["topic"].(string); ok && v != "" {
		out.TopicURL = v
	} else if v, ok := doc["criteria"].(string); ok && v != "" {
		out.TopicURL = v
	}

	// Channel: prefer top-level `channelType`/`endpoint`/`content`,
	// fall back to nested `channel`.
	if ct, ok := doc["channelType"].(map[string]any); ok {
		if code, _ := ct["code"].(string); code != "" {
			out.ChannelType = code
		}
	}
	if ep, ok := doc["endpoint"].(string); ok && ep != "" {
		out.Endpoint = ep
	}
	if c, ok := doc["content"].(string); ok && c != "" {
		out.Content = c
	}
	if mc, ok := doc["maxCount"].(float64); ok && mc > 0 {
		out.MaxCount = int32(mc)
	}
	if hp, ok := doc["heartbeatPeriod"].(float64); ok && hp > 0 {
		d := time.Duration(int64(hp)) * time.Second
		out.HeartbeatPeriod = &d
	}
	if to, ok := doc["timeout"].(float64); ok && to > 0 {
		d := time.Duration(int64(to)) * time.Second
		out.Timeout = &d
	}

	if ch, ok := doc["channel"].(map[string]any); ok {
		if out.ChannelType == "" {
			if t, _ := ch["type"].(string); t != "" {
				out.ChannelType = t
			}
		}
		if out.Endpoint == "" {
			if e, _ := ch["endpoint"].(string); e != "" {
				out.Endpoint = e
			}
		}
		if payload, _ := ch["payload"].(string); payload != "" {
			// R4B Backport `payload` is the content type, but per the
			// IG it's actually the content string. We map via a small
			// normalization: known empty/id-only/full-resource codes
			// map directly; anything else is treated as id-only with
			// the contentType captured separately. The schema allows
			// arbitrary strings so we accept what comes.
			normalized := strings.ToLower(strings.TrimSpace(payload))
			switch normalized {
			case "empty", "id-only", "full-resource":
				out.Content = normalized
			}
		}
		if hp, ok := ch["heartbeatPeriod"].(float64); ok && hp > 0 {
			d := time.Duration(int64(hp)) * time.Second
			out.HeartbeatPeriod = &d
		}
		if to, ok := ch["timeout"].(float64); ok && to > 0 {
			d := time.Duration(int64(to)) * time.Second
			out.Timeout = &d
		}
		if hdrs, ok := ch["header"]; ok {
			b, _ := json.Marshal(hdrs)
			out.Header = b
		}
	}

	if fb, ok := doc["filterBy"]; ok {
		b, _ := json.Marshal(fb)
		out.FilterBy = b
	}
	if c, ok := doc["contact"]; ok {
		b, _ := json.Marshal(c)
		out.Contact = b
	}
	return out, nil
}

// toRow projects the internal model onto a SubscriptionRow ready for
// repos.SubscriptionsRepo.Insert. clientID and status are set by the
// caller since they come from the auth principal and the lifecycle.
func (s *internalSubscription) toRow(clientID string, status repos.SubscriptionStatus) repos.SubscriptionRow {
	row := repos.SubscriptionRow{
		ClientID:        clientID,
		Status:          status,
		TopicURL:        s.TopicURL,
		ChannelType:     s.ChannelType,
		Endpoint:        s.Endpoint,
		Header:          s.Header,
		FilterBy:        s.FilterBy,
		Content:         s.Content,
		HeartbeatPeriod: s.HeartbeatPeriod,
		Timeout:         s.Timeout,
		MaxCount:        s.MaxCount,
		Reason:          s.Reason,
		Contact:         s.Contact,
		EndTime:         s.EndTime,
	}
	return row
}

// renderSubscription produces an R5-shaped JSON body for the response.
// The version-shim is the boundary between internal and wire form;
// for now the handlers emit the R5 native shape.
func renderSubscription(row *repos.SubscriptionRow) []byte {
	out := map[string]any{
		"resourceType": "Subscription",
		"id":           row.ID.String(),
		"status":       string(row.Status),
		"topic":        row.TopicURL,
		"channelType":  map[string]any{"code": row.ChannelType},
		"endpoint":     row.Endpoint,
		"content":      row.Content,
		"maxCount":     row.MaxCount,
	}
	if row.HeartbeatPeriod != nil {
		out["heartbeatPeriod"] = int(row.HeartbeatPeriod.Seconds())
	}
	if row.Timeout != nil {
		out["timeout"] = int(row.Timeout.Seconds())
	}
	if len(row.Header) > 0 {
		var hdrs any
		if err := json.Unmarshal(row.Header, &hdrs); err == nil {
			out["header"] = hdrs
		}
	}
	if len(row.FilterBy) > 0 {
		var fb any
		if err := json.Unmarshal(row.FilterBy, &fb); err == nil {
			out["filterBy"] = fb
		}
	}
	if len(row.Contact) > 0 {
		var c any
		if err := json.Unmarshal(row.Contact, &c); err == nil {
			out["contact"] = c
		}
	}
	if row.Reason != "" {
		out["reason"] = row.Reason
	}
	if row.EndTime != nil {
		out["end"] = row.EndTime.UTC().Format(time.RFC3339)
	}
	if row.Error != "" {
		out["error"] = row.Error
	}
	b, _ := json.Marshal(out)
	return b
}
