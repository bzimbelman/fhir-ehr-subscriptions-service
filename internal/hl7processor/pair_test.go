// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// TestDeriveClassifyExt_NoKey: empty correlation key never participates.
func TestDeriveClassifyExt_NoKey(t *testing.T) {
	t.Parallel()
	ext := deriveClassifyExt(spi.Classification{Kind: spi.ChangeCreate, CorrelationKey: ""}, "ServiceRequest")
	if ext.IsCancellationHalf || ext.IsReplacementHalf {
		t.Fatalf("expected neither half flag with empty key, got %+v", ext)
	}
	if ext.CorrelationKey != "" {
		t.Fatalf("expected empty key, got %q", ext.CorrelationKey)
	}
}

// TestDeriveClassifyExt_DeleteWithKey: delete + key => cancellation half.
func TestDeriveClassifyExt_DeleteWithKey(t *testing.T) {
	t.Parallel()
	ext := deriveClassifyExt(spi.Classification{Kind: spi.ChangeDelete, CorrelationKey: "k1"}, "ServiceRequest")
	if !ext.IsCancellationHalf {
		t.Fatalf("expected IsCancellationHalf=true, got %+v", ext)
	}
	if ext.IsReplacementHalf {
		t.Fatalf("expected IsReplacementHalf=false, got %+v", ext)
	}
}

// TestDeriveClassifyExt_CreateWithKey: create + key => replacement half.
func TestDeriveClassifyExt_CreateWithKey(t *testing.T) {
	t.Parallel()
	ext := deriveClassifyExt(spi.Classification{Kind: spi.ChangeCreate, CorrelationKey: "k1"}, "ServiceRequest")
	if !ext.IsReplacementHalf {
		t.Fatalf("expected IsReplacementHalf=true, got %+v", ext)
	}
	if ext.IsCancellationHalf {
		t.Fatalf("expected IsCancellationHalf=false, got %+v", ext)
	}
}

// TestDeriveClassifyExt_UpdateDropsKey: per LLD §4.5, Update never pairs;
// even if a vendor returns a key with kind=Update, the framework drops it.
func TestDeriveClassifyExt_UpdateDropsKey(t *testing.T) {
	t.Parallel()
	ext := deriveClassifyExt(spi.Classification{Kind: spi.ChangeUpdate, CorrelationKey: "k1"}, "ServiceRequest")
	if ext.CorrelationKey != "" {
		t.Fatalf("expected key dropped on Update, got %q", ext.CorrelationKey)
	}
	if ext.IsCancellationHalf || ext.IsReplacementHalf {
		t.Fatalf("expected no half flags, got %+v", ext)
	}
}

// TestBuildPlainChange: emit a plain ResourceChange from a translated message.
func TestBuildPlainChange(t *testing.T) {
	t.Parallel()
	corr := uuid.New()
	occurred := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	resource := spi.FhirResource{
		ResourceType: "ServiceRequest",
		ID:           "abc",
		Body:         []byte(`{"resourceType":"ServiceRequest","id":"abc"}`),
	}
	c := classifyExt{Kind: spi.ChangeCreate, ResourceType: "ServiceRequest"}
	ch := buildPlainChange(c, resource, corr, occurred)

	if ch.ChangeKind != spi.ChangeCreate {
		t.Fatalf("expected create, got %s", ch.ChangeKind)
	}
	if ch.ResourceType != "ServiceRequest" {
		t.Fatalf("resource type: %q", ch.ResourceType)
	}
	if ch.CorrelationID != corr {
		t.Fatalf("correlation_id: %s", ch.CorrelationID)
	}
	if !ch.OccurredAt.Equal(occurred) {
		t.Fatalf("occurred_at: %v", ch.OccurredAt)
	}
	if ch.PreviousResource != nil {
		t.Fatalf("expected no previous_resource for plain create, got %+v", *ch.PreviousResource)
	}
}

// TestMergePair_DeletePlusCreate: held cancellation + arriving replacement
// builds a single update with cancellation as previous_resource.
func TestMergePair_DeletePlusCreate(t *testing.T) {
	t.Parallel()
	cancelResource := spi.FhirResource{
		ResourceType: "ServiceRequest",
		ID:           "old-id",
		Body:         []byte(`{"resourceType":"ServiceRequest","id":"old-id","status":"revoked"}`),
	}
	replaceResource := spi.FhirResource{
		ResourceType: "ServiceRequest",
		ID:           "new-id",
		Body:         []byte(`{"resourceType":"ServiceRequest","id":"new-id","status":"active"}`),
	}
	heldCorr := uuid.New()
	created := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	occurred := time.Date(2026, 6, 18, 12, 0, 30, 0, time.UTC)

	merged, err := mergePair(spi.ChangeDelete, cancelResource, spi.ChangeCreate, replaceResource, "ServiceRequest", heldCorr, created, occurred)
	if err != nil {
		t.Fatalf("mergePair: %v", err)
	}
	if merged.ChangeKind != spi.ChangeUpdate {
		t.Fatalf("expected update, got %s", merged.ChangeKind)
	}
	if merged.PreviousResource == nil {
		t.Fatal("expected previous_resource set")
	}
	if merged.PreviousResource.ID != "old-id" {
		t.Fatalf("previous id: %q", merged.PreviousResource.ID)
	}
	if merged.Resource.ID != "new-id" {
		t.Fatalf("current id: %q", merged.Resource.ID)
	}
	if merged.CorrelationID != heldCorr {
		t.Fatalf("correlation_id must be the held half's; got %s vs %s", merged.CorrelationID, heldCorr)
	}
	if !merged.OccurredAt.Equal(occurred) {
		t.Fatalf("occurred_at: want max(created, msh7) = %v, got %v", occurred, merged.OccurredAt)
	}
}

// TestMergePair_CreatePlusDelete: held replacement + arriving cancellation
// builds a single update with cancellation as previous_resource.
func TestMergePair_CreatePlusDelete(t *testing.T) {
	t.Parallel()
	heldReplace := spi.FhirResource{ResourceType: "ServiceRequest", ID: "new-id"}
	arrivingCancel := spi.FhirResource{ResourceType: "ServiceRequest", ID: "old-id"}
	heldCorr := uuid.New()
	created := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	occurred := time.Date(2026, 6, 18, 12, 0, 30, 0, time.UTC)

	merged, err := mergePair(spi.ChangeCreate, heldReplace, spi.ChangeDelete, arrivingCancel, "ServiceRequest", heldCorr, created, occurred)
	if err != nil {
		t.Fatalf("mergePair: %v", err)
	}
	if merged.ChangeKind != spi.ChangeUpdate {
		t.Fatalf("expected update, got %s", merged.ChangeKind)
	}
	if merged.PreviousResource == nil || merged.PreviousResource.ID != "old-id" {
		t.Fatalf("previous should be cancellation: %+v", merged.PreviousResource)
	}
	if merged.Resource.ID != "new-id" {
		t.Fatalf("current id: %q", merged.Resource.ID)
	}
}

// TestMergePair_SameKind: defensive case — same-kind pair returns an
// error (caller emits plain on the arriving message).
func TestMergePair_SameKind(t *testing.T) {
	t.Parallel()
	r1 := spi.FhirResource{ResourceType: "ServiceRequest", ID: "a"}
	r2 := spi.FhirResource{ResourceType: "ServiceRequest", ID: "b"}
	_, err := mergePair(spi.ChangeDelete, r1, spi.ChangeDelete, r2, "ServiceRequest", uuid.New(), time.Now(), time.Now())
	if err == nil {
		t.Fatal("expected error for same-kind pair")
	}
}
