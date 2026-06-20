// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"net/http"
	"strings"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/demosupport/resthook"
)

// receiver is the demo's rest-hook listener. It wraps the
// internal/demosupport/resthook receiver (which journals deliveries)
// and tees each inbound POST through a printer so the operator sees
// one line per Bundle.
//
// OP #158: this binary is operator-facing and intentionally avoids
// importing e2e/* packages so a future build tag on test scaffolding
// does not break the demo CLI build.
type receiver struct {
	hook    *resthook.Receiver
	printer *printer
}

func newReceiver(w io.Writer, pretty, noColor bool) *receiver {
	return &receiver{
		hook:    resthook.NewReceiver(),
		printer: newPrinter(w, pretty, noColor),
	}
}

// Handler returns the http.Handler that should be served on the
// demo's local listener. POSTs to `/hook/{id}` are journaled by the
// underlying receiver AND pretty-printed.
func (r *receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	hookHandler := r.hook.Handler()

	mux.HandleFunc("/hook/", func(w http.ResponseWriter, req *http.Request) {
		// Read once, then re-feed both the journaler and the printer.
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		_ = req.Body.Close()
		// Re-issue the request to the journaler so its journal /
		// assert/notification_received endpoints keep working.
		req2 := req.Clone(req.Context())
		req2.Body = io.NopCloser(strings.NewReader(string(body)))
		hookHandler.ServeHTTP(w, req2)

		// Tee through the printer. A handshake (Bundle.type=handshake)
		// has no notificationEvent / Patient — printNotification still
		// emits a one-line "(unknown) ... event=0" for it, which is
		// the right thing for the operator to see.
		h, err := extractBundleHighlights(body)
		if err != nil {
			r.printer.printError("malformed delivery body: %v", err)
			return
		}
		r.printer.printNotification(h)
	})
	// Pass-through control-plane endpoints so callers can still query
	// the journal during a demo run (handy for screenshots).
	mux.Handle("/received", hookHandler)
	mux.Handle("/received/", hookHandler)
	mux.Handle("/journal", hookHandler)
	mux.Handle("/assert/notification_received", hookHandler)
	return mux
}
