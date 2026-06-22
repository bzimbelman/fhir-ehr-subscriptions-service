// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command test-receivers is the consolidated channel-receiver fixture
// the e2e/realstack docker-compose stack uses. It rolls four legacy
// per-channel binaries plus the third-party mailpit container into one
// process exposing four ports concurrently:
//
//	:8090 — rest-hook subscriber (HTTP)
//	  POST   /hook/{subscription_id}    — delivery target
//	  POST   /notify/{subscription_id}  — alias
//	  POST   /program/{tag}             — install response program
//	  DELETE /program/{tag}             — clear response program
//	  GET    /notifications             — full journal
//	  GET    /notifications/{sub_id}    — filtered by subscription id
//	  POST   /reset                     — clear journal
//	  GET    /healthz                   — liveness probe
//
//	:8091 — websocket subscriber (HTTP query API)
//	  GET    /events                    — full journal
//	  GET    /events/{subscription_id}  — filtered by subscription id
//	  POST   /reset                     — clear journal
//	  GET    /healthz                   — liveness probe
//	  WS connection logic is opt-in via WS_URL / WS_BINDING_TOKEN /
//	  WS_SUBSCRIPTION_TOPIC env vars (same as the legacy binary).
//
//	:8093 — MLLP scripted control plane (HTTP)
//	  POST   /scenarios/admit_patient
//	  POST   /scenarios/place_order
//	  POST   /scenarios/finalize_lab
//	  POST   /scenarios/cancel_and_replace_order
//	  POST   /scenarios/burst_messages
//	  GET    /healthz
//	  GET    /target
//	The control plane synthesizes HL7 v2 frames and emits them over
//	real TCP to FHIRSUBS_MLLP_TARGET. EnableMLLP gates startup of
//	this subsystem so non-MLLP runs skip it.
//
//	:1025 — SMTP receiver (real github.com/emersion/go-smtp server)
//	  Captures every accepted SMTP message + headers + envelope.
//	:1080 — SMTP query API
//	  GET    /messages                  — full journal
//	  GET    /messages?to=<rcpt>        — filter by recipient
//	  POST   /reset                     — clear journal
//	  GET    /healthz                   — liveness probe
//
// Replaces:
//   - cmd/test-resthook-subscriber
//   - cmd/test-ws-subscriber
//   - cmd/test-mllp-control-plane
//   - mailpit container in docker-compose
//
// One binary, one Dockerfile, one compose service. See
// e2e/realstack/fixtures/test-receivers/Dockerfile.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// receiversConfig configures the consolidated binary. Each address
// follows the host:port convention; ":0" picks an ephemeral port
// (handy for tests, never set by the docker fixture).
type receiversConfig struct {
	RestHookAddr string // bind for the rest-hook HTTP listener (:8090)
	WSQueryAddr  string // bind for the WS query API listener (:8091)
	MLLPHTTPAddr string // bind for the MLLP control-plane HTTP listener (:8093)
	SMTPListen   string // bind for the SMTP listener (:1025)
	SMTPQuery    string // bind for the SMTP query API listener (:1080)

	// MLLPTarget is the host:port of the prod binary's MLLP listener.
	// Empty disables the MLLP subsystem (non-MLLP test runs skip it).
	MLLPTarget string

	// WSDialURL / WSDialToken / WSDialTopic are read from env in the
	// docker fixture but kept here so tests that bring up the WS
	// connector loop can pass them programmatically. Empty WSDialURL
	// leaves the WS subscriber idle (query API still serves).
	WSDialURL   string
	WSDialToken string
	WSDialTopic string
}

// receivers is the consolidated fixture handle. Start brings every
// configured subsystem up; Close tears them down.
type receivers struct {
	cfg receiversConfig

	resthookL net.Listener
	resthookS *http.Server
	wsL       net.Listener
	wsS       *http.Server
	mllpL     net.Listener
	mllpS     *http.Server
	smtpRx    *smtpReceiver
	wsCtx     context.Context
	wsCancel  context.CancelFunc

	closeOnce sync.Once
}

// newReceivers constructs the orchestrator. Listeners are NOT opened
// until Start.
func newReceivers(cfg receiversConfig) (*receivers, error) {
	if cfg.RestHookAddr == "" {
		return nil, fmt.Errorf("test-receivers: RestHookAddr required")
	}
	if cfg.WSQueryAddr == "" {
		return nil, fmt.Errorf("test-receivers: WSQueryAddr required")
	}
	if cfg.SMTPListen == "" {
		return nil, fmt.Errorf("test-receivers: SMTPListen required")
	}
	if cfg.SMTPQuery == "" {
		return nil, fmt.Errorf("test-receivers: SMTPQuery required")
	}
	// MLLPHTTPAddr is required only when MLLPTarget is set; the docker
	// fixture always sets both, but the test harness runs without the
	// MLLP subsystem in some scenarios.
	return &receivers{cfg: cfg}, nil
}

// Start opens listeners and begins serving every configured subsystem.
// Returns the first bind error encountered; partial state is cleaned
// up on failure so the caller never sees half-started receivers.
func (r *receivers) Start() error {
	if err := r.startRestHook(); err != nil {
		r.Close()
		return err
	}
	if err := r.startWS(); err != nil {
		r.Close()
		return err
	}
	if r.cfg.MLLPTarget != "" {
		if err := r.startMLLP(); err != nil {
			r.Close()
			return err
		}
	}
	if err := r.startSMTP(); err != nil {
		r.Close()
		return err
	}
	return nil
}

func (r *receivers) startRestHook() error {
	l, err := net.Listen("tcp", r.cfg.RestHookAddr)
	if err != nil {
		return fmt.Errorf("rest-hook listen %s: %w", r.cfg.RestHookAddr, err)
	}
	r.resthookL = l
	jr := newJournal()
	r.resthookS = &http.Server{
		Handler:           buildMux(jr),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = r.resthookS.Serve(l) }()
	log.Printf("test-receivers: rest-hook listening on %s", l.Addr().String())
	return nil
}

func (r *receivers) startWS() error {
	l, err := net.Listen("tcp", r.cfg.WSQueryAddr)
	if err != nil {
		return fmt.Errorf("ws query listen %s: %w", r.cfg.WSQueryAddr, err)
	}
	r.wsL = l
	jr := &wsJournal{wire: os.Stdout}
	r.wsS = &http.Server{
		Handler:           buildWSMux(jr),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = r.wsS.Serve(l) }()
	log.Printf("test-receivers: ws query API listening on %s", l.Addr().String())

	if r.cfg.WSDialURL != "" {
		ctx, cancel := context.WithCancel(context.Background())
		r.wsCtx = ctx
		r.wsCancel = cancel
		go connectAndReceive(ctx, r.cfg.WSDialURL, r.cfg.WSDialToken, r.cfg.WSDialTopic, jr)
		log.Printf("test-receivers: ws subscriber dialing %s", r.cfg.WSDialURL)
	}
	return nil
}

func (r *receivers) startMLLP() error {
	if r.cfg.MLLPHTTPAddr == "" {
		return fmt.Errorf("test-receivers: MLLPTarget set but MLLPHTTPAddr empty")
	}
	l, err := net.Listen("tcp", r.cfg.MLLPHTTPAddr)
	if err != nil {
		return fmt.Errorf("mllp http listen %s: %w", r.cfg.MLLPHTTPAddr, err)
	}
	r.mllpL = l
	cp := &controlPlane{
		target: r.cfg.MLLPTarget,
		client: newMLLPClient(r.cfg.MLLPTarget),
	}
	r.mllpS = &http.Server{
		Handler:           buildMLLPMux(cp),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = r.mllpS.Serve(l) }()
	log.Printf("test-receivers: mllp control plane listening on %s, target %s", l.Addr().String(), r.cfg.MLLPTarget)
	return nil
}

func (r *receivers) startSMTP() error {
	rcv, err := newSMTPReceiver(r.cfg.SMTPListen, r.cfg.SMTPQuery)
	if err != nil {
		return err
	}
	if err := rcv.Start(); err != nil {
		return err
	}
	r.smtpRx = rcv
	// Log resolved bound addresses (the configured values may use ":0"
	// in tests; the kernel-assigned ports show up only after Listen).
	smtpAddr := rcv.smtpListener.Addr().String()
	queryAddr := rcv.httpListener.Addr().String()
	log.Printf("test-receivers: smtp listening on %s, query API on %s", smtpAddr, queryAddr)
	return nil
}

// Close shuts every subsystem. Safe to call multiple times.
func (r *receivers) Close() {
	r.closeOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if r.wsCancel != nil {
			r.wsCancel()
		}
		if r.resthookS != nil {
			_ = r.resthookS.Shutdown(shutdownCtx)
		}
		if r.wsS != nil {
			_ = r.wsS.Shutdown(shutdownCtx)
		}
		if r.mllpS != nil {
			_ = r.mllpS.Shutdown(shutdownCtx)
		}
		if r.smtpRx != nil {
			r.smtpRx.Close()
		}
		// Listeners are closed by Shutdown; the explicit closes here
		// guard against partial-startup paths where a Server was
		// never wired.
		for _, l := range []net.Listener{r.resthookL, r.wsL, r.mllpL} {
			if l != nil {
				_ = l.Close()
			}
		}
	})
}

func main() {
	resthookAddr := flag.String("resthook-addr", ":8090", "rest-hook HTTP listener address")
	wsQueryAddr := flag.String("ws-query-addr", ":8091", "websocket query API listener address")
	mllpHTTPAddr := flag.String("mllp-http-addr", ":8093", "MLLP control-plane HTTP listener address")
	smtpListen := flag.String("smtp-addr", ":1025", "SMTP listener address")
	smtpQuery := flag.String("smtp-query-addr", ":1080", "SMTP query API listener address")
	mllpTarget := flag.String("mllp-target", os.Getenv("FHIRSUBS_MLLP_TARGET"), "MLLP target host:port (empty disables MLLP subsystem)")
	flag.Parse()

	cfg := receiversConfig{
		RestHookAddr: *resthookAddr,
		WSQueryAddr:  *wsQueryAddr,
		MLLPHTTPAddr: *mllpHTTPAddr,
		SMTPListen:   *smtpListen,
		SMTPQuery:    *smtpQuery,
		MLLPTarget:   *mllpTarget,
		WSDialURL:    os.Getenv("WS_URL"),
		WSDialToken:  os.Getenv("WS_BINDING_TOKEN"),
		WSDialTopic:  os.Getenv("WS_SUBSCRIPTION_TOPIC"),
	}

	r, err := newReceivers(cfg)
	if err != nil {
		log.Fatalf("test-receivers: %v", err)
	}
	if err := r.Start(); err != nil {
		log.Fatalf("test-receivers: start: %v", err)
	}
	defer r.Close()

	// Block on signal — docker stop will SIGTERM the process.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("test-receivers: shutting down")
}
