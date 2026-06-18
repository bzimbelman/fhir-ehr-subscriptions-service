// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// spawnActivate runs the channel-handshake goroutine with three guards
// the bare `go s.activate(...)` call lacked (B-10):
//
//  1. The goroutine joins Deps.ActivationWaitGroup so the lifecycle
//     module can wait out in-flight handshakes during shutdown.
//  2. The ctx is derived from Deps.LifecycleCtx with
//     Deps.ActivationTimeout so a slow vendor cannot pin the goroutine
//     and its DB connection forever.
//  3. A deferred recover() converts any panic in the channel adapter
//     into a metric increment + audit event + status-error update,
//     instead of crashing the process.
func (s *server) spawnActivate(id uuid.UUID) {
	if wg := s.deps.ActivationWaitGroup; wg != nil {
		wg.Add(1)
	}
	go s.runActivate(id)
}

func (s *server) runActivate(id uuid.UUID) {
	if wg := s.deps.ActivationWaitGroup; wg != nil {
		defer wg.Done()
	}

	parent := s.deps.LifecycleCtx
	if parent == nil {
		parent = context.Background()
	}
	timeout := s.deps.ActivationTimeout
	if timeout <= 0 {
		timeout = DefaultActivationTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			s.recordActivatePanic()
			slog.Error("activation panic recovered",
				"subscription_id", id.String(),
				"panic", fmt.Sprintf("%v", r))
			// Best-effort: flip to error so the row does not stay stuck
			// at requested. Use a fresh background ctx because the
			// per-call ctx may already be canceled.
			_ = s.deps.Subscriptions.UpdateStatus(context.Background(), id,
				repos.SubError, "activation panic")
			_ = s.deps.Audit.Append(context.Background(),
				"subscription.handshake.fail", id.String(), "failure", nil, nil)
		}
	}()

	s.activate(ctx, id)
}

func (s *server) recordActivatePanic() {
	if s.deps.Metrics == nil {
		return
	}
	if r, ok := s.deps.Metrics.(ActivatePanicRecorder); ok {
		r.RecordActivatePanic()
	}
}
