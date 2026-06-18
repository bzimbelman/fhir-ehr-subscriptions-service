// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// ControlPlaneConfig parameterizes a control plane instance.
type ControlPlaneConfig struct {
	// MLLPTarget is the addr:port of the MLLP listener (typically
	// fhir-subs' listener, or a test echo listener) that scenarios send
	// HL7 frames to.
	MLLPTarget string

	// SendTimeout caps each MLLP send. Default 2s.
	SendTimeout time.Duration
}

// ControlPlane is the orchestrator-facing HTTP control surface for the
// EHR mock. Each scenario is a POST endpoint that takes a small JSON
// config and emits one or more HL7 messages over MLLP.
type ControlPlane struct {
	cfg    ControlPlaneConfig
	client *MLLPClient
}

// NewControlPlane returns a control plane wired to send via MLLP at
// cfg.MLLPTarget.
func NewControlPlane(cfg ControlPlaneConfig) *ControlPlane {
	if cfg.SendTimeout == 0 {
		cfg.SendTimeout = 2 * time.Second
	}
	return &ControlPlane{
		cfg:    cfg,
		client: NewMLLPClient(cfg.MLLPTarget),
	}
}

// RegisteredScenarios returns the canonical scenario name list.
func (c *ControlPlane) RegisteredScenarios() []string {
	out := []string{
		"admit_patient",
		"place_order",
		"finalize_lab",
		"cancel_and_replace_order",
		"burst_messages",
	}
	sort.Strings(out)
	return out
}

// Handler returns the scenario HTTP handler.
func (c *ControlPlane) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/scenarios/admit_patient", c.handleAdmitPatient)
	mux.HandleFunc("/scenarios/place_order", c.handlePlaceOrder)
	mux.HandleFunc("/scenarios/finalize_lab", c.handleFinalizeLab)
	mux.HandleFunc("/scenarios/cancel_and_replace_order", c.handleCancelAndReplace)
	mux.HandleFunc("/scenarios/burst_messages", c.handleBurstMessages)
	return mux
}

type admitPatientReq struct {
	PatientID   string `json:"patient_id"`
	MessageID   string `json:"message_id"`
	Trigger     string `json:"trigger"`
	FamilyName  string `json:"family_name"`
	GivenName   string `json:"given_name"`
}

func (c *ControlPlane) handleAdmitPatient(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body admitPatientReq
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	msg := BuildADT(ADTOptions{
		TriggerEvent:  body.Trigger,
		MessageID:     body.MessageID,
		PatientID:     body.PatientID,
		PatientFamily: body.FamilyName,
		PatientGiven:  body.GivenName,
	})
	c.send(w, req, []byte(msg))
}

type placeOrderReq struct {
	PlacerOrderID  string `json:"placer_order_id"`
	FillerOrderID  string `json:"filler_order_id"`
	PatientID      string `json:"patient_id"`
	MessageID      string `json:"message_id"`
	UniversalSvcID string `json:"universal_svc_id"`
}

func (c *ControlPlane) handlePlaceOrder(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body placeOrderReq
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	msg := BuildORM(ORMOptions{
		ControlCode:    ORCControlNew,
		MessageID:      body.MessageID,
		PlacerOrderID:  body.PlacerOrderID,
		FillerOrderID:  body.FillerOrderID,
		PatientID:      body.PatientID,
		UniversalSvcID: body.UniversalSvcID,
	})
	c.send(w, req, []byte(msg))
}

type finalizeLabReq struct {
	MessageID     string `json:"message_id"`
	PatientID     string `json:"patient_id"`
	ObservationID string `json:"observation_id"`
	Value         string `json:"value"`
	Unit          string `json:"unit"`
	RefRange      string `json:"reference_range"`
	AbnormalFlag  string `json:"abnormal_flag"`
}

func (c *ControlPlane) handleFinalizeLab(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body finalizeLabReq
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	msg := BuildORU(ORUOptions{
		MessageID: body.MessageID,
		PatientID: body.PatientID,
		Result: ORUResult{
			ObservationID: body.ObservationID,
			Value:         body.Value,
			Unit:          body.Unit,
			RefRange:      body.RefRange,
			AbnormalFlag:  body.AbnormalFlag,
		},
	})
	c.send(w, req, []byte(msg))
}

type cancelAndReplaceReq struct {
	PlacerOrderID         string `json:"placer_order_id"`
	FillerOrderID         string `json:"filler_order_id"`
	PatientID             string `json:"patient_id"`
	CancelMessageID       string `json:"cancel_message_id"`
	ReplacementMessageID  string `json:"replacement_message_id"`
}

func (c *ControlPlane) handleCancelAndReplace(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body cancelAndReplaceReq
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	cancelMsg := BuildORM(ORMOptions{
		ControlCode:   ORCControlCancel,
		MessageID:     body.CancelMessageID,
		PlacerOrderID: body.PlacerOrderID,
		FillerOrderID: body.FillerOrderID,
		PatientID:     body.PatientID,
	})
	replaceMsg := BuildORM(ORMOptions{
		ControlCode:   ORCControlNew,
		MessageID:     body.ReplacementMessageID,
		PlacerOrderID: body.PlacerOrderID,
		FillerOrderID: body.FillerOrderID,
		PatientID:     body.PatientID,
	})

	ctx, cancel := context.WithTimeout(req.Context(), c.cfg.SendTimeout*2)
	defer cancel()
	if _, err := c.client.Send(ctx, []byte(cancelMsg)); err != nil {
		http.Error(w, "send cancel: "+err.Error(), http.StatusBadGateway)
		return
	}
	if _, err := c.client.Send(ctx, []byte(replaceMsg)); err != nil {
		http.Error(w, "send replace: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"emitted":2}`))
}

type burstReq struct {
	Count     int    `json:"count"`
	Trigger   string `json:"trigger"`
	PatientID string `json:"patient_id"`
}

func (c *ControlPlane) handleBurstMessages(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body burstReq
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Count <= 0 {
		http.Error(w, "count must be > 0", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(),
		time.Duration(body.Count)*c.cfg.SendTimeout)
	defer cancel()
	for i := 0; i < body.Count; i++ {
		msg := BuildADT(ADTOptions{
			TriggerEvent: body.Trigger,
			MessageID:    fmt.Sprintf("BURST-%d", i),
			PatientID:    body.PatientID,
		})
		if _, err := c.client.Send(ctx, []byte(msg)); err != nil {
			http.Error(w, "send burst: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"emitted":%d}`, body.Count)))
}

// send is the shared exit path for one-shot scenario emitters: send the
// pre-built HL7 body and surface ACK errors as 502.
func (c *ControlPlane) send(w http.ResponseWriter, req *http.Request, body []byte) {
	ctx, cancel := context.WithTimeout(req.Context(), c.cfg.SendTimeout)
	defer cancel()
	ack, err := c.client.Send(ctx, body)
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
