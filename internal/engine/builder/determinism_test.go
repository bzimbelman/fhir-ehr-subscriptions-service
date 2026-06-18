// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package builder_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestBuildBundleDeterminism encodes the same job 100 times and asserts
// the resulting bytes are byte-identical. This pins audit B-28: a
// hash-chained audit log over bundle bytes must produce stable hashes
// for stable inputs.
func TestBuildBundleDeterminism(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	subID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	corr := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	ev1ID := uuid.MustParse("00000000-0000-0000-0000-000000000010")
	ev2ID := uuid.MustParse("00000000-0000-0000-0000-000000000011")

	sub := repos.SubscriptionRow{
		ID: subID, TopicURL: "http://example.org/t",
		Status: repos.SubActive, Content: "full-resource",
	}
	ev1 := repos.EhrEventRow{
		ID: ev1ID, TopicURL: "http://example.org/t",
		Focus: "X/1", ChangeKind: repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"X","id":"1"}`),
		CorrelationID: corr, OccurredAt: fixed,
	}
	ev2 := repos.EhrEventRow{
		ID: ev2ID, TopicURL: "http://example.org/t",
		Focus: "X/2", ChangeKind: repos.ChangeUpdate,
		Resource:      []byte(`{"resourceType":"X","id":"2"}`),
		CorrelationID: corr, OccurredAt: fixed.Add(time.Second),
	}

	b := builder.New(builder.Config{Clock: func() time.Time { return fixed }})
	job := builder.Job{
		Subscription:     sub,
		NotificationType: channel.BundleEventNotification,
		Events:           []repos.EhrEventRow{ev1, ev2},
		PerSubEventNumbers: map[uuid.UUID]int64{
			ev1ID: 1, ev2ID: 2,
		},
		CorrelationIDOverride: corr.String(),
	}

	const N = 100
	hashes := make(map[string]int, 1)
	var first []byte
	for i := 0; i < N; i++ {
		env, err := b.Build(context.Background(), job)
		if err != nil {
			t.Fatalf("Build iter %d: %v", i, err)
		}
		sum := sha256.Sum256(env.BundleBytes)
		hashes[hex.EncodeToString(sum[:])]++
		if first == nil {
			first = env.BundleBytes
			continue
		}
		if string(env.BundleBytes) != string(first) {
			t.Fatalf("iter %d differs from iter 0\nfirst: %s\nthis : %s",
				i, first, env.BundleBytes)
		}
	}
	if len(hashes) != 1 {
		t.Fatalf("expected exactly one bundle digest across %d builds, got %d", N, len(hashes))
	}
}
