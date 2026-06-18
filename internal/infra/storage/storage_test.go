// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage"
)

func TestStorageConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := storage.Config{
		PostgresURL: "postgres://localhost/x",
		KeyVersions: map[int32][]byte{1: make32()},
		ActiveKey:   1,
	}
	cfg.ApplyDefaults()
	if cfg.Pool.MaxConnections == 0 {
		t.Error("pool defaults not applied")
	}
	if cfg.Retention.Hl7MessageQueue == 0 {
		t.Error("retention defaults not applied")
	}
	if cfg.Partitioning.RunInterval == 0 {
		t.Error("partition defaults not applied")
	}
}

func TestStorageStartReturnsErrorWithBadURL(t *testing.T) {
	t.Parallel()

	cfg := storage.Config{
		PostgresURL: "this-is-bogus://nope",
		KeyVersions: map[int32][]byte{1: make32()},
		ActiveKey:   1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := storage.Start(ctx, cfg, storage.Context{}); err == nil {
		t.Fatal("expected error from bad URL")
	}
}

func TestStorageStartRequiresKeys(t *testing.T) {
	t.Parallel()

	cfg := storage.Config{
		PostgresURL: "postgres://localhost/x",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := storage.Start(ctx, cfg, storage.Context{}); err == nil {
		t.Fatal("expected error when no keys configured")
	}
}

func make32() []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}
