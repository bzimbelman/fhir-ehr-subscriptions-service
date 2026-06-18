// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestE2E_Builder_BundleBytesAreDeterministic pins B-28: the same
// notification job MUST encode to byte-identical Bundle JSON across
// 100 invocations. The audit-chain hashes the bundle bytes; map
// iteration randomization in encoding/json would otherwise give
// different hashes on identical input.
func TestE2E_Builder_BundleBytesAreDeterministic(t *testing.T) {
	t.Parallel()
	_ = requireHarness(t)
	_ = context.Background()

	fixed := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	subID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	corr := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	ev1ID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	ev2ID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
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
	var firstSum [32]byte
	for i := 0; i < N; i++ {
		env, err := b.Build(context.Background(), job)
		if err != nil {
			t.Fatalf("Build %d: %v", i, err)
		}
		sum := sha256.Sum256(env.BundleBytes)
		if i == 0 {
			firstSum = sum
			continue
		}
		if sum != firstSum {
			t.Fatalf("iter %d differs (sum=%x vs %x); bundle bytes:\n%s",
				i, sum[:], firstSum[:], env.BundleBytes)
		}
	}
}
