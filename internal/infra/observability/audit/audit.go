// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package audit is a placeholder.
package audit

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Event is a placeholder.
type Event struct {
	OccurredAt    time.Time
	ActorKind     string
	ActorID       string
	Action        string
	TargetKind    string
	TargetID      string
	Outcome       string
	CorrelationID uuid.UUID
	Payload       map[string]any
}

// Row is a placeholder.
type Row struct {
	OccurredAt    time.Time
	ActorKind     string
	ActorID       string
	Action        string
	TargetKind    string
	TargetID      string
	Outcome       string
	CorrelationID uuid.UUID
	Payload       map[string]any
	PriorHash     []byte
	ChainInput    []byte
	ChainHash     []byte
}

// Store is a placeholder.
type Store interface {
	InsertAuditRow(ctx context.Context, row Row) error
	LastChainHash(ctx context.Context) ([]byte, error)
	AcquireChainLock(ctx context.Context) (func() error, error)
	IterateRows(ctx context.Context, fn func(Row) error) error
}

// Sink is a placeholder.
type Sink interface {
	Emit(ctx context.Context, evt Event) error
}

// WriterOptions is a placeholder.
type WriterOptions struct {
	Store         Store
	Sink          Sink
	Clock         func() time.Time
	OnSinkFailure func(sink string)
}

// Writer is a placeholder.
type Writer struct{}

// NewWriter is a placeholder.
func NewWriter(WriterOptions) (*Writer, error) {
	return nil, errors.New("not yet implemented")
}

// Emit is a placeholder.
func (*Writer) Emit(context.Context, Event) error { return errors.New("not yet implemented") }

// CanonicalizeJSON is a placeholder.
func CanonicalizeJSON([]byte) ([]byte, error) {
	return nil, errors.New("not yet implemented")
}

// GenesisHash is a placeholder.
func GenesisHash() []byte { return nil }

// VerifyChain is a placeholder.
func VerifyChain(context.Context, Store) error { return errors.New("not yet implemented") }

// NewStdoutSink is a placeholder.
func NewStdoutSink() Sink { return nil }

// NewWriterSink is a placeholder.
func NewWriterSink(string, *sync.Mutex, io.Writer) Sink { return nil }
