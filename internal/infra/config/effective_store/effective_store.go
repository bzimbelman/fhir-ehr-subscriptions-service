// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package effectivestore holds the published effective configuration snapshot
// behind an atomic.Pointer. Consumers read through Read() and get a cheap
// clone of the immutable struct; reload swaps via Store() so in-flight reads
// see either the old or the new snapshot consistently.
//
// Per docs/low-level-design/configuration.md S11.
package effectivestore

import (
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
)

// MaxConcurrentNotifications caps in-flight subscriber callbacks so a
// notification storm cannot fork-bomb the runtime. Excess callbacks
// queue on a buffered channel and are dispatched in FIFO order. S-15 #5.
const MaxConcurrentNotifications = 32

// notifyPanicLogger is the package-level slog handler used for panic
// reports from subscriber callbacks. Tests override via
// SetNotifyPanicLogger; production wires it to the host logger from
// config.Start so panics are observable rather than swallowed.
var (
	notifyPanicLoggerMu sync.RWMutex
	notifyPanicLogger   *slog.Logger
)

// SetNotifyPanicLogger installs a slog handler used to report panics
// in subscriber callbacks. nil disables logging.
func SetNotifyPanicLogger(l *slog.Logger) {
	notifyPanicLoggerMu.Lock()
	notifyPanicLogger = l
	notifyPanicLoggerMu.Unlock()
}

func panicLogger() *slog.Logger {
	notifyPanicLoggerMu.RLock()
	defer notifyPanicLoggerMu.RUnlock()
	return notifyPanicLogger
}

// Effective is the typed, immutable, post-validation, post-resolution snapshot
// the rest of the service reads. The opaque map matches the architecture's
// canonical YAML one-to-one (see docs/architecture.md "Configuration"). The
// store does not interpret the tree — it passes it through.
//
// Sub-domain typed structs live in config_types/. The orchestrator wiring
// (config.Module.Start) populates the typed view; consumers either reach into
// the typed view or read the generic Tree for ad-hoc cases.
type Effective struct {
	// Tree is the post-resolution generic config tree.
	Tree map[string]interface{}
	// Redaction is the path-keyed sensitivity map. Travels with the snapshot.
	Redaction *redaction.Map
}

// Store wraps atomic.Pointer[Effective] with subscription bookkeeping. It is
// safe for concurrent use.
type Store struct {
	ptr     atomic.Pointer[Effective]
	subs    sync.Map // domain -> *subList
	notifyC chan func()
	once    sync.Once
}

// New returns an empty Store. The caller publishes the first snapshot via
// Publish before consumers Read.
func New() *Store { return &Store{} }

// startWorkerPool lazily spins up a bounded pool of dispatcher
// goroutines. The pool is created on the first Publish so an
// effective-store with no subscribers (test fixtures) does not pay the
// goroutine cost.
func (s *Store) startWorkerPool() {
	s.once.Do(func() {
		s.notifyC = make(chan func(), MaxConcurrentNotifications)
		for i := 0; i < MaxConcurrentNotifications; i++ {
			go func() {
				for fn := range s.notifyC {
					runWithRecover(fn)
				}
			}()
		}
	})
}

// runWithRecover invokes fn and logs any panic via the package-level
// slog handler so a buggy subscriber does not bring down the
// notification dispatcher.
func runWithRecover(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			if l := panicLogger(); l != nil {
				l.Error("effective_store: subscriber panic",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())))
			}
		}
	}()
	fn()
}

// Publish swaps in a fresh snapshot. Subscribers for matching domains are
// notified after the swap completes.
//
// Notifications dispatch through a bounded worker pool (size
// MaxConcurrentNotifications) instead of fork-bombing one goroutine
// per callback per Publish. A buggy subscriber's panic is logged and
// recovered so the dispatcher continues to drain. S-15 #5.
func (s *Store) Publish(eff *Effective) {
	s.ptr.Store(eff)
	s.notifyAll(eff)
}

// Read returns the current snapshot. May be nil before the first Publish.
func (s *Store) Read() *Effective {
	return s.ptr.Load()
}

// SubscriptionID identifies a registered callback so Unsubscribe can remove it.
type SubscriptionID uint64

// Subscribe registers cb to be invoked after every Publish that touches the
// named domain (or "" for any). Notifications are best-effort: the store does
// not block reload while a slow subscriber processes a notification.
func (s *Store) Subscribe(domain string, cb func(*Effective)) SubscriptionID {
	listAny, _ := s.subs.LoadOrStore(domain, &subList{})
	list := listAny.(*subList)
	return list.add(cb)
}

// Unsubscribe removes a previously-registered callback. No-op if id is unknown.
func (s *Store) Unsubscribe(domain string, id SubscriptionID) {
	if v, ok := s.subs.Load(domain); ok {
		v.(*subList).remove(id)
	}
}

func (s *Store) notifyAll(eff *Effective) {
	s.startWorkerPool()
	dispatch := func(fn func()) {
		// Try a non-blocking send; if the pool is saturated, fall
		// back to running inline on the publisher goroutine. This
		// keeps notifications best-effort and ordered without
		// unbounded queueing under storms.
		select {
		case s.notifyC <- fn:
		default:
			runWithRecover(fn)
		}
	}
	s.subs.Range(func(_, v interface{}) bool {
		v.(*subList).notify(eff, dispatch)
		return true
	})
}

type subList struct {
	mu    sync.Mutex
	next  SubscriptionID
	items map[SubscriptionID]func(*Effective)
}

func (l *subList) add(cb func(*Effective)) SubscriptionID {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.items == nil {
		l.items = map[SubscriptionID]func(*Effective){}
	}
	l.next++
	id := l.next
	l.items[id] = cb
	return id
}

func (l *subList) remove(id SubscriptionID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.items, id)
}

func (l *subList) notify(eff *Effective, dispatch func(func())) {
	l.mu.Lock()
	cbs := make([]func(*Effective), 0, len(l.items))
	for _, cb := range l.items {
		cbs = append(cbs, cb)
	}
	l.mu.Unlock()
	// Dispatch through the supplied executor (the store's bounded pool
	// in production) so slow subscribers cannot block each other but
	// cannot fork-bomb the runtime either.
	for _, cb := range cbs {
		cb := cb
		dispatch(func() { cb(eff) })
	}
}
