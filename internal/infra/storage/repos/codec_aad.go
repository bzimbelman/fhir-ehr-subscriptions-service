// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
)

// AAD construction helpers per encrypted table.
//
// Each helper returns the AAD blob to pass to codec.Encrypt / codec.Decrypt
// for that table's row. AAD binds the ciphertext to the row's identity
// so an operator with raw DB write access cannot swap envelopes between
// rows or tables and have the receiving row decrypt successfully.
//
// Composite primary keys are joined with a 0x1F (Unit Separator) byte,
// which cannot appear in any of the textual PK columns we use.

const aadCompositeSep = 0x1F

func uuidBytes(id uuid.UUID) []byte {
	b := id[:]
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// AADHl7MessageQueue binds raw_body to (table, id, key_version).
func AADHl7MessageQueue(id uuid.UUID, kv int32) []byte {
	return codec.BuildAAD("hl7_message_queue", uuidBytes(id), kv)
}

// AADPendingPairs binds pending_resource to (table, correlation_key|listener_endpoint, key_version).
func AADPendingPairs(correlationKey, listenerEndpoint string, kv int32) []byte {
	rowKey := make([]byte, 0, len(correlationKey)+1+len(listenerEndpoint))
	rowKey = append(rowKey, correlationKey...)
	rowKey = append(rowKey, aadCompositeSep)
	rowKey = append(rowKey, listenerEndpoint...)
	return codec.BuildAAD("pending_pairs", rowKey, kv)
}

// AADResourceChanges binds resource & previous_resource to (table, id, key_version).
// The "field" suffix lets us bind separate AAD for the resource vs
// previous_resource columns of the same row, so the two ciphertexts on
// one row are not interchangeable either.
func AADResourceChanges(id uuid.UUID, kv int32, field string) []byte {
	rowKey := make([]byte, 0, 16+1+len(field))
	rowKey = append(rowKey, uuidBytes(id)...)
	rowKey = append(rowKey, aadCompositeSep)
	rowKey = append(rowKey, field...)
	return codec.BuildAAD("resource_changes", rowKey, kv)
}

// AADEhrEvents binds resource & previous_resource to (table, id, key_version, field).
func AADEhrEvents(id uuid.UUID, kv int32, field string) []byte {
	rowKey := make([]byte, 0, 16+1+len(field))
	rowKey = append(rowKey, uuidBytes(id)...)
	rowKey = append(rowKey, aadCompositeSep)
	rowKey = append(rowKey, field...)
	return codec.BuildAAD("ehr_events", rowKey, kv)
}

// AADDeadLetters binds payload_redacted to (table, id, key_version).
func AADDeadLetters(id uuid.UUID, kv int32) []byte {
	return codec.BuildAAD("dead_letters", uuidBytes(id), kv)
}
