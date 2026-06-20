// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command test-mllp-control-plane is a real HTTP service that
// synthesizes HL7 v2 frames and emits them over MLLP, on real TCP
// sockets, to a target MLLP listener. It runs inside the H1
// realstack docker-compose stack and replaces the in-process
// e2e/mockehr.ControlPlane fake.
//
// The binary's HTTP control surface is identical to the legacy
// in-process ControlPlane so existing legacy-harness scenarios port
// cleanly: each scenario is a POST endpoint that decodes a small
// JSON config and emits one or more HL7 messages over MLLP.
//
//	POST /scenarios/admit_patient            — emits ADT^Axx
//	POST /scenarios/place_order              — emits ORM^O01 (NW)
//	POST /scenarios/finalize_lab             — emits ORU^R01
//	POST /scenarios/cancel_and_replace_order — emits CA + NW pair
//	POST /scenarios/burst_messages           — emits N ADT frames in one call
//	GET  /healthz                            — liveness probe
//	GET  /target                             — returns the configured MLLP target
//
// MLLP target is configured via -target host:port or
// FHIRSUBS_MLLP_TARGET env. The service does not retain any per-scenario
// state — every request opens a new TCP connection to target, writes one
// MLLP-framed body, reads the listener's ACK, and returns it on the
// HTTP response.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", ":8093", "HTTP listen address")
	target := flag.String("target", os.Getenv("FHIRSUBS_MLLP_TARGET"), "MLLP target host:port")
	flag.Parse()

	if *target == "" {
		log.Fatalf("test-mllp-control-plane: no MLLP target configured (set -target or FHIRSUBS_MLLP_TARGET)")
	}

	cp := &controlPlane{
		target: *target,
		client: newMLLPClient(*target),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"target": *target})
	})
	mux.HandleFunc("/scenarios/admit_patient", cp.handleAdmitPatient)
	mux.HandleFunc("/scenarios/place_order", cp.handlePlaceOrder)
	mux.HandleFunc("/scenarios/finalize_lab", cp.handleFinalizeLab)
	mux.HandleFunc("/scenarios/cancel_and_replace_order", cp.handleCancelAndReplace)
	mux.HandleFunc("/scenarios/burst_messages", cp.handleBurstMessages)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("test-mllp-control-plane: listening on %s, MLLP target %s", *addr, *target)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// controlPlane is the HTTP handler set. Each scenario method decodes a
// small JSON body, builds the HL7 frame, and rounds it through the MLLP
// client. The send path is the same exit point shared by the in-process
// reference implementation in e2e/mockehr/scenarios.go.
type controlPlane struct {
	target string
	client *mllpClient
}

const sendTimeout = 5 * time.Second

func (c *controlPlane) handleAdmitPatient(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PatientID  string `json:"patient_id"`
		MessageID  string `json:"message_id"`
		Trigger    string `json:"trigger"`
		FamilyName string `json:"family_name"`
		GivenName  string `json:"given_name"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	msg := buildADT(adtOptions{
		TriggerEvent:  body.Trigger,
		MessageID:     body.MessageID,
		PatientID:     body.PatientID,
		PatientFamily: body.FamilyName,
		PatientGiven:  body.GivenName,
	})
	c.send(w, req, []byte(msg))
}

func (c *controlPlane) handlePlaceOrder(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PlacerOrderID  string `json:"placer_order_id"`
		FillerOrderID  string `json:"filler_order_id"`
		PatientID      string `json:"patient_id"`
		MessageID      string `json:"message_id"`
		UniversalSvcID string `json:"universal_svc_id"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	msg := buildORM(ormOptions{
		ControlCode:    "NW",
		MessageID:      body.MessageID,
		PlacerOrderID:  body.PlacerOrderID,
		FillerOrderID:  body.FillerOrderID,
		PatientID:      body.PatientID,
		UniversalSvcID: body.UniversalSvcID,
	})
	c.send(w, req, []byte(msg))
}

func (c *controlPlane) handleFinalizeLab(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		MessageID     string `json:"message_id"`
		PatientID     string `json:"patient_id"`
		ObservationID string `json:"observation_id"`
		Value         string `json:"value"`
		Unit          string `json:"unit"`
		RefRange      string `json:"reference_range"`
		AbnormalFlag  string `json:"abnormal_flag"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	msg := buildORU(oruOptions{
		MessageID:     body.MessageID,
		PatientID:     body.PatientID,
		ObservationID: body.ObservationID,
		Value:         body.Value,
		Unit:          body.Unit,
		RefRange:      body.RefRange,
		AbnormalFlag:  body.AbnormalFlag,
	})
	c.send(w, req, []byte(msg))
}

func (c *controlPlane) handleCancelAndReplace(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PlacerOrderID        string `json:"placer_order_id"`
		FillerOrderID        string `json:"filler_order_id"`
		PatientID            string `json:"patient_id"`
		CancelMessageID      string `json:"cancel_message_id"`
		ReplacementMessageID string `json:"replacement_message_id"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	cancelMsg := buildORM(ormOptions{
		ControlCode:   "CA",
		MessageID:     body.CancelMessageID,
		PlacerOrderID: body.PlacerOrderID,
		FillerOrderID: body.FillerOrderID,
		PatientID:     body.PatientID,
	})
	replaceMsg := buildORM(ormOptions{
		ControlCode:   "NW",
		MessageID:     body.ReplacementMessageID,
		PlacerOrderID: body.PlacerOrderID,
		FillerOrderID: body.FillerOrderID,
		PatientID:     body.PatientID,
	})
	ctx, cancel := context.WithTimeout(req.Context(), sendTimeout*2)
	defer cancel()
	if _, err := c.client.send(ctx, []byte(cancelMsg)); err != nil {
		http.Error(w, "send cancel: "+err.Error(), http.StatusBadGateway)
		return
	}
	if _, err := c.client.send(ctx, []byte(replaceMsg)); err != nil {
		http.Error(w, "send replace: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"emitted":2}`))
}

func (c *controlPlane) handleBurstMessages(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Count     int    `json:"count"`
		Trigger   string `json:"trigger"`
		PatientID string `json:"patient_id"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Count <= 0 {
		http.Error(w, "count must be > 0", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(),
		time.Duration(body.Count)*sendTimeout)
	defer cancel()
	for i := 0; i < body.Count; i++ {
		msg := buildADT(adtOptions{
			TriggerEvent: body.Trigger,
			MessageID:    fmt.Sprintf("BURST-%d", i),
			PatientID:    body.PatientID,
		})
		if _, err := c.client.send(ctx, []byte(msg)); err != nil {
			http.Error(w, "send burst: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, `{"emitted":%d}`, body.Count)
}

func (c *controlPlane) send(w http.ResponseWriter, req *http.Request, body []byte) {
	ctx, cancel := context.WithTimeout(req.Context(), sendTimeout)
	defer cancel()
	ack, err := c.client.send(ctx, body)
	if err != nil {
		http.Error(w, "mllp send: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"ack": string(ack),
	})
}

// MLLP framing constants per HL7 LLD.
const (
	mllpStartBlock byte = 0x0B
	mllpEndBlock   byte = 0x1C
	mllpCR         byte = 0x0D
)

// mllpClient dials target on every send and round-trips one message.
type mllpClient struct {
	addr        string
	dialTimeout time.Duration
	ioTimeout   time.Duration
	mu          sync.Mutex
}

func newMLLPClient(addr string) *mllpClient {
	return &mllpClient{
		addr:        addr,
		dialTimeout: 2 * time.Second,
		ioTimeout:   5 * time.Second,
	}
}

func (c *mllpClient) send(ctx context.Context, body []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	dialer := net.Dialer{Timeout: c.dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.addr, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(c.ioTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	framed := make([]byte, 0, len(body)+3)
	framed = append(framed, mllpStartBlock)
	framed = append(framed, body...)
	framed = append(framed, mllpEndBlock, mllpCR)
	if _, werr := conn.Write(framed); werr != nil {
		return nil, fmt.Errorf("write frame: %w", werr)
	}

	var acc bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, rerr := conn.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
		}
		if bytes.Contains(acc.Bytes(), []byte{mllpEndBlock, mllpCR}) {
			break
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return nil, fmt.Errorf("read ACK: %w", rerr)
		}
	}
	startIdx := bytes.IndexByte(acc.Bytes(), mllpStartBlock)
	if startIdx < 0 {
		return nil, errors.New("missing MLLP start block")
	}
	endIdx := bytes.LastIndex(acc.Bytes(), []byte{mllpEndBlock, mllpCR})
	if endIdx < 0 || endIdx <= startIdx {
		return nil, errors.New("missing or misordered MLLP end block")
	}
	return acc.Bytes()[startIdx+1 : endIdx], nil
}

// HL7 v2 builder constants.
const (
	defaultSendingApp      = "MOCKEHR"
	defaultSendingFacility = "E2E"
	defaultReceivingApp    = "FHIRSUBS"
	defaultReceiving       = "TEST"
	defaultProcessingID    = "T"
	defaultVersionID       = "2.5.1"
	segTerm                = "\r"
)

type adtOptions struct {
	TriggerEvent  string
	MessageID     string
	PatientID     string
	PatientFamily string
	PatientGiven  string
}

func buildADT(o adtOptions) string {
	if o.TriggerEvent == "" {
		o.TriggerEvent = "A01"
	}
	now := time.Now().UTC()
	msh := buildMSH("ADT^"+o.TriggerEvent, o.MessageID, now)
	evn := fmt.Sprintf("EVN|%s|%s", o.TriggerEvent, fmtTS(now))
	family := orDefault(o.PatientFamily, "Doe")
	given := orDefault(o.PatientGiven, "Jane")
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||%s^%s", o.PatientID, defaultSendingFacility, family, given)
	return joinSegments(msh, evn, pid)
}

type ormOptions struct {
	ControlCode    string
	MessageID      string
	PlacerOrderID  string
	FillerOrderID  string
	PatientID      string
	UniversalSvcID string
}

func buildORM(o ormOptions) string {
	if o.ControlCode == "" {
		o.ControlCode = "NW"
	}
	now := time.Now().UTC()
	msh := buildMSH("ORM^O01", o.MessageID, now)
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||Doe^Jane", orDefault(o.PatientID, "MRN0"), defaultSendingFacility)
	orc := fmt.Sprintf("ORC|%s|%s|%s|||%s", o.ControlCode, o.PlacerOrderID, o.FillerOrderID, fmtTS(now))
	universal := orDefault(o.UniversalSvcID, "TEST^Test Order^L")
	obr := fmt.Sprintf("OBR|1|%s|%s|%s|||%s", o.PlacerOrderID, o.FillerOrderID, universal, fmtTS(now))
	return joinSegments(msh, pid, orc, obr)
}

type oruOptions struct {
	MessageID     string
	PatientID     string
	ObservationID string
	Value         string
	Unit          string
	RefRange      string
	AbnormalFlag  string
}

func buildORU(o oruOptions) string {
	now := time.Now().UTC()
	msh := buildMSH("ORU^R01", o.MessageID, now)
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||Doe^Jane", o.PatientID, defaultSendingFacility)
	obr := fmt.Sprintf("OBR|1|||%s|||%s", o.ObservationID, fmtTS(now))
	obx := fmt.Sprintf("OBX|1|NM|%s||%s|%s|%s|%s|||F",
		o.ObservationID, o.Value, o.Unit, o.RefRange, o.AbnormalFlag)
	return joinSegments(msh, pid, obr, obx)
}

func buildMSH(messageType, controlID string, now time.Time) string {
	return strings.Join([]string{
		"MSH",
		"^~\\&",
		defaultSendingApp,
		defaultSendingFacility,
		defaultReceivingApp,
		defaultReceiving,
		fmtTS(now),
		"",
		messageType,
		controlID,
		defaultProcessingID,
		defaultVersionID,
	}, "|")
}

func joinSegments(segs ...string) string {
	var sb strings.Builder
	for _, s := range segs {
		sb.WriteString(s)
		sb.WriteString(segTerm)
	}
	return sb.String()
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func fmtTS(t time.Time) string {
	return t.UTC().Format("20060102150405")
}
