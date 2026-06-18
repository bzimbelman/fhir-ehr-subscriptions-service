// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package hl7processor implements Stage 1 of the EHR pipeline for HL7 v2
// inputs. It claims unprocessed rows from `hl7_message_queue`, drives them
// through the four overridable translation steps an [spi.Hl7MessageProcessor]
// implementation provides (lex, classify, map, validate), enforces the
// cancel-and-replace correlation hold window via `pending_pairs`, and writes
// either a `resource_changes` row (success), a `pending_pairs` row (held
// half), or a `dead_letters` row (terminal failure) — all transactionally
// consistent with marking the source row processed.
//
// The package owns its claim loop and the expiry reaper; vendor adapters
// supply only the four translation steps via the SPI interface.
package hl7processor

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// ErrorClass classifies a terminal translation failure for dead-letter
// routing and metric labelling per LLD §3.
type ErrorClass string

// ErrorClass values per LLD §3.
const (
	ErrorClassParse      ErrorClass = "parse"
	ErrorClassClassify   ErrorClass = "classify"
	ErrorClassMap        ErrorClass = "map"
	ErrorClassValidation ErrorClass = "validation"
	ErrorClassUnexpected ErrorClass = "unexpected"
	// ErrorClassTxBeginFailed marks a row whose processOne BeginTx
	// repeatedly failed past Config.MaxRowAttempts (S-9.9). Rows in
	// this class never finished translation — there is no FHIR
	// resource — so [dlKindForClass] routes them to `hl7_unparseable`.
	ErrorClassTxBeginFailed ErrorClass = "tx_begin_failed"
)

// String returns the wire form used in metric labels and structured logs.
func (c ErrorClass) String() string { return string(c) }

// outcomeKind tags a [processingOutcome] with its decision.
type outcomeKind int

const (
	outcomeEmitted outcomeKind = iota + 1
	outcomeHeld
	outcomeResolved
	outcomeDeadLetter
)

// processingOutcome is the in-process result of translating one queue row.
// LLD §3 documents the four variants. The struct is intentionally a sum
// type via the kind tag rather than an interface so the caller can pattern
// match in one switch and so the zero value is invalid.
type processingOutcome struct {
	kind outcomeKind

	// Emitted: a single resource_changes row is to be written; the source
	// row is to be marked processed.
	emitted spi.ResourceChange

	// Held: the message is the first half of a cancel-and-replace pair;
	// pending_pairs gets a row, the source row stays unprocessed.
	held heldPair

	// Resolved: the message is the second half of a cancel-and-replace pair
	// already held in pending_pairs; both source rows mark processed, the
	// pending row deletes, one merged update goes to resource_changes.
	resolved resolvedPair
}

// heldPair is the in-process shape of a pending_pairs row to be inserted.
type heldPair struct {
	CorrelationKey   string
	ListenerEndpoint string
	Resource         spi.FhirResource
	PendingKind      spi.ChangeKind // ChangeDelete (held cancellation) or ChangeCreate (held replacement)
	SourceMessageID  uuid.UUID
	ExpiresAt        time.Time
	CreatedAt        time.Time
	ResourceType     string
	CorrelationID    uuid.UUID
}

// resolvedPair packages the merged change that resolves a held half with
// the just-translated half. PartnerSourceID is the queue row id of the
// previously-held half, marked processed in the same tx.
type resolvedPair struct {
	Merged                spi.ResourceChange
	PartnerSourceID       uuid.UUID
	ClearCorrelationKey   string
	ClearListenerEndpoint string
}

// translateError tags an error with the [ErrorClass] that should drive
// dead-letter routing. The translate() helper converts panics in vendor
// code and returned errors from each step into one of these.
type translateError struct {
	Class ErrorClass
	Err   error
}

func (e *translateError) Error() string {
	if e == nil || e.Err == nil {
		return "translate: <nil>"
	}
	return fmt.Sprintf("translate: %s: %s", e.Class, e.Err.Error())
}

func (e *translateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// asTranslateError returns the [*translateError] wrapped in err, or nil.
func asTranslateError(err error) *translateError {
	var te *translateError
	if errors.As(err, &te) {
		return te
	}
	return nil
}

// classifyExt is the framework's view of a vendor's [spi.Classification]
// once the framework has filled in the fields the SPI does not expose.
//
// The SPI today returns only (Kind, CorrelationKey). LLD §3 calls for two
// extra signal flags (is_cancellation_half, is_replacement_half) and the
// resource_type, all of which the framework derives from the SPI return
// + the message body. We compute them here so the rest of the pipeline
// can stay strict.
type classifyExt struct {
	Kind               spi.ChangeKind
	CorrelationKey     string
	IsCancellationHalf bool
	IsReplacementHalf  bool
	ResourceType       string
}

// deriveClassifyExt promotes a vendor classification to the framework
// view. A non-empty CorrelationKey on a Delete is the cancellation half;
// on a Create it is the replacement half. Update never participates in
// pairing per LLD §4.5.
func deriveClassifyExt(c spi.Classification, resourceType string) classifyExt {
	out := classifyExt{
		Kind:           c.Kind,
		CorrelationKey: c.CorrelationKey,
		ResourceType:   resourceType,
	}
	if c.CorrelationKey == "" {
		return out
	}
	switch c.Kind {
	case spi.ChangeDelete:
		out.IsCancellationHalf = true
	case spi.ChangeCreate:
		out.IsReplacementHalf = true
	default:
		// Update with a non-empty key is treated as non-pairing per LLD §4.5
		// (Update never pairs). Drop the key so resolve_pairing emits plain.
		out.CorrelationKey = ""
	}
	return out
}
