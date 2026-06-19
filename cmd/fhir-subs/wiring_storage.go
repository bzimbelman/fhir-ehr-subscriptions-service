// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/pool"
)

// decodeCodecKeys validates and base64-decodes the YAML codec key
// bundle into the version->bytes map storage.Config.KeyVersions
// expects. Story #95: storage.Start owns the codec construction now,
// so this helper is shared between the wiring path (storage.Start) and
// the audit-CLI path (which still builds a freestanding codec).
func decodeCodecKeys(cfg CodecConfig) (map[int32][]byte, error) {
	if len(cfg.Keys) == 0 {
		return nil, errors.New("at least one key required")
	}
	if cfg.ActiveKeyVersion == 0 {
		return nil, errors.New("active_key_version is required")
	}
	keys := make(map[int32][]byte, len(cfg.Keys))
	for _, k := range cfg.Keys {
		if k.Version == 0 {
			return nil, fmt.Errorf("key entry missing version")
		}
		raw, err := base64.StdEncoding.DecodeString(k.Material)
		if err != nil {
			return nil, fmt.Errorf("key v%d: base64 decode: %w", k.Version, err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("key v%d: want 32 bytes, got %d", k.Version, len(raw))
		}
		keys[k.Version] = raw
	}
	if _, ok := keys[cfg.ActiveKeyVersion]; !ok {
		return nil, fmt.Errorf("active_key_version=%d not present in keys[]", cfg.ActiveKeyVersion)
	}
	return keys, nil
}

// buildStorageConfig translates the cmd-side YAML config into the
// storage.Config bundle storage.Start consumes. Operator-supplied
// storage.retention.* and storage.partitioning.* values pass through;
// zero values fall back to storage.Config.ApplyDefaults inside
// storage.Start.
func buildStorageConfig(cfg *Config, keys map[int32][]byte) storage.Config {
	return storage.Config{
		PostgresURL: cfg.Database.URL,
		Pool: pool.Config{
			ApplicationName: "fhir-ehr-subscriptions-service",
		},
		KeyVersions: keys,
		ActiveKey:   cfg.Codec.ActiveKeyVersion,
		Retention: storage.RetentionConfig{
			Hl7MessageQueue: cfg.Storage.Retention.Hl7MessageQueue,
			Deliveries:      cfg.Storage.Retention.Deliveries,
			DeadLetters:     cfg.Storage.Retention.DeadLetters,
			AuditLog:        cfg.Storage.Retention.AuditLog,
			RunInterval:     cfg.Storage.Retention.RunInterval,
			BatchSize:       cfg.Storage.Retention.BatchSize,
			BatchPause:      cfg.Storage.Retention.BatchPause,
			TickTimeout:     cfg.Storage.Retention.TickTimeout,
		},
		Partitioning: storage.PartitionConfig{
			AutoDrop:                 cfg.Storage.Partitioning.AutoDrop,
			PartitionLockTimeout:     cfg.Storage.Partitioning.PartitionLockTimeout,
			RunInterval:              cfg.Storage.Partitioning.RunInterval,
			TickTimeout:              cfg.Storage.Partitioning.TickTimeout,
			ResourceChangesRetention: cfg.Storage.Partitioning.ResourceChangesRetention,
			EhrEventsRetention:       cfg.Storage.Partitioning.EhrEventsRetention,
		},
		Lifecycle: storage.LifecycleConfig{
			ShutdownGracePeriod: cfg.Lifecycle.ShutdownGracePeriod,
		},
	}
}
