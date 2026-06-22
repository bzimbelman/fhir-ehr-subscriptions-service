// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"sync"
	"time"

	"github.com/emersion/go-smtp"
)

// capturedSMTPMessage is one journaled inbound SMTP delivery.
//
// JSON shape is the contract realstack email-channel tests assert
// against; fields are tagged so a rename surfaces as a downstream
// compile / decode break.
type capturedSMTPMessage struct {
	From       string            `json:"from"`
	Recipients []string          `json:"recipients"`
	Headers    map[string]string `json:"headers"`
	Data       string            `json:"data"`
	ReceivedAt time.Time         `json:"received_at"`
}

// smtpReceiver runs a real github.com/emersion/go-smtp server on a
// configured listen address and exposes a journal + REST query API on
// a configured query address.
//
// Replaces the third-party mailpit container. The receiver is real
// software — go-smtp's Server is the production SMTP path the
// project's e2e/mocksub already uses for in-process tests, and the
// query API is a strict subset of mailpit's REST shape (GET /messages,
// GET /messages?to=<addr>, POST /reset). No mocks of SMTP anywhere.
type smtpReceiver struct {
	listenAddr string
	queryAddr  string

	mu   sync.Mutex
	all  []capturedSMTPMessage
	wire io.Writer

	srv   *smtp.Server
	httpS *http.Server

	smtpListener net.Listener
	httpListener net.Listener
}

// newSMTPReceiver constructs a receiver bound to the given listen +
// query addresses. Listeners are NOT opened until Start.
func newSMTPReceiver(listenAddr, queryAddr string) (*smtpReceiver, error) {
	if listenAddr == "" {
		return nil, fmt.Errorf("smtp receiver: listen address required")
	}
	if queryAddr == "" {
		return nil, fmt.Errorf("smtp receiver: query address required")
	}
	r := &smtpReceiver{
		listenAddr: listenAddr,
		queryAddr:  queryAddr,
	}

	be := &smtpBackend{r: r}
	s := smtp.NewServer(be)
	s.Domain = "test-receivers.local"
	s.AllowInsecureAuth = true
	s.ReadTimeout = 30 * time.Second
	s.WriteTimeout = 30 * time.Second
	// 5 MiB ceiling matches the legacy mocksub.FakeSMTP and gives
	// generous headroom for any production-shape email payload while
	// keeping a runaway producer from OOM-ing the test container.
	s.MaxMessageBytes = 5 * 1024 * 1024
	s.MaxRecipients = 50
	r.srv = s

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", r.handleHealthz)
	mux.HandleFunc("/messages", r.handleMessages)
	mux.HandleFunc("/reset", r.handleReset)
	r.httpS = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return r, nil
}

// Start opens both listeners and begins serving. Start does not block;
// errors during Serve are surfaced via listener close (Start fails
// only if the binds fail).
func (r *smtpReceiver) Start() error {
	smtpL, err := net.Listen("tcp", r.listenAddr)
	if err != nil {
		return fmt.Errorf("smtp listen %s: %w", r.listenAddr, err)
	}
	r.smtpListener = smtpL
	r.srv.Addr = smtpL.Addr().String()

	httpL, err := net.Listen("tcp", r.queryAddr)
	if err != nil {
		_ = smtpL.Close()
		return fmt.Errorf("smtp query listen %s: %w", r.queryAddr, err)
	}
	r.httpListener = httpL

	go func() { _ = r.srv.Serve(smtpL) }()
	go func() { _ = r.httpS.Serve(httpL) }()
	return nil
}

// Close shuts both listeners. Safe to call multiple times.
func (r *smtpReceiver) Close() {
	if r.srv != nil {
		_ = r.srv.Close()
	}
	if r.httpS != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = r.httpS.Shutdown(ctx)
		cancel()
	}
	if r.smtpListener != nil {
		_ = r.smtpListener.Close()
	}
	if r.httpListener != nil {
		_ = r.httpListener.Close()
	}
}

func (r *smtpReceiver) record(msg capturedSMTPMessage) {
	msg.ReceivedAt = time.Now().UTC()
	r.mu.Lock()
	r.all = append(r.all, msg)
	r.mu.Unlock()
	if r.wire != nil {
		_ = json.NewEncoder(r.wire).Encode(msg)
	}
}

func (r *smtpReceiver) snapshot() []capturedSMTPMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedSMTPMessage, len(r.all))
	copy(out, r.all)
	return out
}

func (r *smtpReceiver) resetJournal() {
	r.mu.Lock()
	r.all = nil
	r.mu.Unlock()
}

func (r *smtpReceiver) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (r *smtpReceiver) handleReset(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.resetJournal()
	w.WriteHeader(http.StatusNoContent)
}

func (r *smtpReceiver) handleMessages(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	all := r.snapshot()
	wantRcpt := req.URL.Query().Get("to")
	if wantRcpt != "" {
		filtered := make([]capturedSMTPMessage, 0, len(all))
		for _, m := range all {
			for _, rcpt := range m.Recipients {
				if rcpt == wantRcpt {
					filtered = append(filtered, m)
					break
				}
			}
		}
		all = filtered
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(all)
}

// parseHeadersBufio extracts RFC-5322 headers from the captured DATA
// blob. Duplicate header names keep the last value because the email-
// channel tests assert on Subject / Message-ID / X-* uniqueness; a
// multi-value map would force every call site to thread a slice it
// doesn't currently touch. Tests that need raw ordering can read the
// untouched .Data field.
func parseHeadersBufio(data []byte) map[string]string {
	tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(data)))
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(header))
	for k, v := range header {
		if len(v) > 0 {
			out[k] = v[len(v)-1]
		}
	}
	return out
}

// --- go-smtp Backend / Session -------------------------------------------

type smtpBackend struct {
	r *smtpReceiver
}

func (b *smtpBackend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &smtpSession{r: b.r}, nil
}

type smtpSession struct {
	r          *smtpReceiver
	from       string
	recipients []string
}

func (s *smtpSession) AuthPlain(username, password string) error { return nil }

func (s *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.recipients = append(s.recipients, to)
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.r.record(capturedSMTPMessage{
		From:       s.from,
		Recipients: append([]string(nil), s.recipients...),
		Headers:    parseHeadersBufio(body),
		Data:       string(body),
	})
	return nil
}

func (s *smtpSession) Reset() {
	s.from = ""
	s.recipients = nil
}

func (s *smtpSession) Logout() error { return nil }
