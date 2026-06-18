// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package effectivestore holds the published effective configuration snapshot
// behind an atomic.Pointer. Consumers read through Read() and get a cheap
// clone of the immutable struct; reload swaps via Store() so in-flight reads
// see either the old or the new snapshot consistently.
//
// Per docs/low-level-design/configuration.md S11.
package effectivestore

import (
	"sync"
	"sync/atomic"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/config/redaction"
)

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
	ptr  atomic.Pointer[Effective]
	subs sync.Map // domain -> *subList
}

// New returns an empty Store. The caller publishes the first snapshot via
// Publish before consumers Read.
func New() *Store { return &Store{} }

// Publish swaps in a fresh snapshot. Subscribers for matching domains are
// notified after the swap completes.
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
	s.subs.Range(func(_, v interface{}) bool {
		v.(*subList).notify(eff)
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

func (l *subList) notify(eff *Effective) {
	l.mu.Lock()
	cbs := make([]func(*Effective), 0, len(l.items))
	for _, cb := range l.items {
		cbs = append(cbs, cb)
	}
	l.mu.Unlock()
	// Run callbacks without the lock so a slow subscriber cannot deadlock
	// future notifications. Each callback is fired in its own goroutine so a
	// single slow subscriber does not block the rest.
	for _, cb := range cbs {
		go cb(eff)
	}
}
