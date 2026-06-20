// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// hl7PassthroughTopicJSON mirrors the seedHL7Topic body so the
// production binary's matcher catalog (loaded from --config topics
// dir) and the API's subscription_topics row stay aligned.
const hl7PassthroughTopicJSON = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topics/hl7-passthrough",
  "version": "1.0.0",
  "title": "HL7 passthrough",
  "status": "active",
  "resourceTrigger": [{
    "resource": "Bundle",
    "supportedInteraction": ["create", "update", "delete"]
  }]
}`

// TestE2E_ProdBinary_ProcessesHL7Message proves the production binary
// — not just the harness — runs the full pipeline end-to-end for the
// default adapter. A Subscription is registered, an HL7 v2 message is
// framed and sent over the binary's MLLP listener, and the rest-hook
// subscriber must receive the resulting notification within a few
// seconds.
//
// What this test pins that the harness Pipeline does NOT:
//
//   - cmd/fhir-subs's startup sequence builds a real
//     mllp.Listener bound to a TCP port,
//   - run.go's pipeline workers (hl7processor, matcher, submatcher,
//     scheduler) actually claim and forward the message,
//   - the MLLP -> hl7_message_queue -> resource_changes ->
//     ehr_events -> deliveries -> rest-hook chain works through the
//     production binary's wiring.
//
// B-4. The vendor-adapter variants live in
// TestE2E_ProdBinary_ProcessesHL7Message_VendorAdapters (OP #149).
func TestE2E_ProdBinary_ProcessesHL7Message(t *testing.T) {
	runProdBinaryHL7E2E(t, prodBinaryHL7Case{
		adapterID: "default",
		// OP #174: Default adapter now runs the shared hl7v2 mapper and
		// emits a real FHIR R4 Bundle (Patient + Encounter for ADT). The
		// parametric harness uses content=id-only subscriptions so the
		// notification body carries the bundle envelope but not the
		// PID round-trip — same harness limitation that gates the
		// vendor-adapter cases. Matching "Bundle" pins pipeline wiring;
		// the round-trip is unit-tested in adapters/default/mapping_test.go.
		bodyMustContain: []string{`"resourceType":"Bundle"`},
		facilityPrefix:  "e2e-prod-hl7",
	})
}

// TestE2E_ProdBinary_ProcessesHL7Message_VendorAdapters parameterizes
// the prod-binary HL7 round-trip over every vendor adapter (OP #149).
// Each subtest boots cmd/fhir-subs with adapter.id set to the vendor
// and asserts that a real HL7 v2 message produces a non-stub Bundle
// notification — i.e., MapToFHIR was actually exercised.
//
// Today every vendor adapter's MapToFHIR returns a hardcoded
// {"resourceType":"Bundle","type":"collection"} (see
// adapters/{cerner,epic,athena,nextgen,meditech,allscripts}/*.go),
// which discards the parsed input. Each subtest is therefore RED until
// its corresponding implementation story lands and is t.Skip'd with
// the OP id of the per-vendor follow-up:
//
//   - cerner     -> OP #168
//   - epic       -> OP #169
//   - athena     -> OP #170
//   - nextgen    -> OP #171
//   - meditech   -> OP #172
//   - allscripts -> OP #173
//
// Removing a t.Skip is the green-bar signal that the matching vendor
// implementation has shipped.
func TestE2E_ProdBinary_ProcessesHL7Message_VendorAdapters(t *testing.T) {
	cases := []prodBinaryHL7Case{
		{
			// OP #168: Cerner MapToFHIR shipped (unit-tested in
			// adapters/cerner/mapping_test.go). The e2e check stays
			// blocked because the parametric test uses content=id-only
			// subscriptions; PATID1234 only round-trips on
			// content=full-resource. Tracked for follow-up.
			adapterID:       "cerner",
			facilityPrefix:  "e2e-prod-hl7-cerner",
			bodyMustContain: []string{"PATID1234"},
			skipReason:      "OP #168 mapping shipped; e2e blocked on content=full-resource harness work",
		},
		{
			// OP #169: Epic MapToFHIR shipped (unit-tested in
			// adapters/epic/mapping_test.go). See cerner case for
			// why the e2e remains skipped.
			adapterID:       "epic",
			facilityPrefix:  "e2e-prod-hl7-epic",
			bodyMustContain: []string{"PATID1234"},
			skipReason:      "OP #169 mapping shipped; e2e blocked on content=full-resource harness work",
		},
		{
			// OP #170: Athena MapToFHIR shipped (unit-tested in
			// adapters/athena/mapping_test.go). E2e blocked on the
			// same content=full-resource harness work as cerner+epic.
			adapterID:       "athena",
			facilityPrefix:  "e2e-prod-hl7-athena",
			bodyMustContain: []string{"PATID1234"},
			skipReason:      "OP #170 mapping shipped; e2e blocked on content=full-resource harness work",
		},
		{
			// OP #171: NextGen MapToFHIR shipped (unit-tested in
			// adapters/nextgen/mapping_test.go).
			adapterID:       "nextgen",
			facilityPrefix:  "e2e-prod-hl7-nextgen",
			bodyMustContain: []string{"PATID1234"},
			skipReason:      "OP #171 mapping shipped; e2e blocked on content=full-resource harness work",
		},
		{
			// OP #172: Meditech MapToFHIR shipped (unit-tested in
			// adapters/meditech/mapping_test.go, including lowercase
			// msh handling). E2e blocked on the same content=full-
			// resource harness work as the rest.
			adapterID:       "meditech",
			facilityPrefix:  "e2e-prod-hl7-meditech",
			bodyMustContain: []string{"PATID1234"},
			skipReason:      "OP #172 mapping shipped; e2e blocked on content=full-resource harness work",
		},
		{
			// OP #173: Allscripts MapToFHIR shipped (unit-tested in
			// adapters/allscripts/mapping_test.go, including pre-2014
			// lowercase msh handling).
			adapterID:       "allscripts",
			facilityPrefix:  "e2e-prod-hl7-allscripts",
			bodyMustContain: []string{"PATID1234"},
			skipReason:      "OP #173 mapping shipped; e2e blocked on content=full-resource harness work",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.adapterID, func(t *testing.T) {
			runProdBinaryHL7E2E(t, tc)
		})
	}
}

// prodBinaryHL7Case parameterizes runProdBinaryHL7E2E over a vendor
// adapter id. skipReason — when non-empty — flags the test as
// blocked on the named per-vendor follow-up OP and triggers t.Skip
// before any harness setup runs.
type prodBinaryHL7Case struct {
	adapterID       string
	facilityPrefix  string
	bodyMustContain []string
	skipReason      string
}

func runProdBinaryHL7E2E(t *testing.T, tc prodBinaryHL7Case) {
	t.Helper()
	if tc.skipReason != "" {
		// OP #149: skipReason cites per-vendor follow-up OP id (#168-#173).
		t.Skip(tc.skipReason)
	}

	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)
	if err := seedHL7Topic(ctx, h.DB); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	// Stage a topic catalog directory so the production binary's
	// in-memory matcher catalog matches the subscription_topics row.
	topicsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(topicsDir, "hl7-passthrough.json"),
		[]byte(hl7PassthroughTopicJSON), 0o600); err != nil {
		t.Fatalf("write topic file: %v", err)
	}

	// 1. Real RSA key + httptest JWKS server. Story #292: prod-binary
	//    e2e boots in production posture and /Subscription POSTs MUST
	//    carry a real bearer.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	kid := uuid.NewString()
	jwksMux := http.NewServeMux()
	jwksMux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwksDocFromPublic(&priv.PublicKey, kid))
	})
	jwksSrv := httptest.NewServer(jwksMux)
	t.Cleanup(jwksSrv.Close)

	clientID := tc.facilityPrefix + "-" + uuid.New().String()[:8]
	if _, seedErr := h.DB.Exec(ctx, `INSERT INTO auth_clients (id, jwks_url, scopes, display_name)
			VALUES ($1, $2, ARRAY['system/Subscription.cruds','system/Subscription.c','system/Subscription.r','system/Subscription.u','system/Subscription.d']::text[], $1)`,
		clientID, jwksSrv.URL+"/jwks"); seedErr != nil {
		t.Fatalf("seed auth_clients: %v", seedErr)
	}

	secretBytes := make([]byte, 32)
	if _, rndErr := rand.Read(secretBytes); rndErr != nil {
		t.Fatalf("rand: %v", rndErr)
	}
	issuedSecret := base64.StdEncoding.EncodeToString(secretBytes)
	audience := "https://" + tc.facilityPrefix + "/token"

	// 2. Start the production binary with the case's adapter id and a
	//    real MLLP listener.
	mllpPort := freePort(t)
	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            tc.facilityPrefix,
		AdapterID:             tc.adapterID,
		MLLPBind:              "127.0.0.1:" + mllpPort,
		Insecure:              true,
		GracePeriod:           5 * time.Second,
		TopicsCatalogDir:      topicsDir,
		AuthAudience:          audience,
		AuthAllowInsecureJWKS: true,
		AuthTokenURL:          audience,
		AuthIssuedSecret:      issuedSecret,
		AuthIssuedIssuer:      tc.facilityPrefix + "-issuer",
		AuthAccessTokenTTL:    5 * time.Minute,
	})
	defer bin.Stop(t, 10*time.Second)

	// 3. Register a subscription via the real /Subscription HTTP API.
	bearerFn := func() string {
		return mintRealBearer(t, ctx, bin.HTTPURL(), audience, clientID, kid, priv)
	}
	subPath := tc.facilityPrefix + "-sub"
	subID, err := RegisterSubscriber(ctx, h, RegisterSubscriberOptions{
		ClientID:        clientID,
		TopicURL:        "http://example.org/topics/hl7-passthrough",
		Endpoint:        "http://" + h.MockSub.HTTPAddr + "/hook/" + subPath,
		APIBaseURL:      bin.HTTPURL(),
		BearerTokenFunc: bearerFn,
	})
	if err != nil {
		t.Fatalf("RegisterSubscriber: %v", err)
	}

	// 4. Send an HL7 v2 message via the binary's MLLP listener.
	sendHL7ToProdBinary(t, ctx, bin.MLLPAddr(), sampleHL7v2)

	// 5. Wait for the rest-hook receiver to record the notification.
	got, err := WaitForNotification(ctx, h, subPath, 30*time.Second)
	if err != nil {
		if pid, parseErr := uuid.Parse(subID); parseErr == nil {
			dumpPipelineState(t, ctx, h, pid)
		} else {
			t.Logf("could not parse subID for dump: %v", parseErr)
		}
		t.Fatalf("WaitForNotification: %v", err)
	}
	if got.SubscriptionID != subPath {
		t.Fatalf("subscription id: got %q, want %q", got.SubscriptionID, subPath)
	}
	for _, want := range tc.bodyMustContain {
		if !bytes.Contains(got.Body, []byte(want)) {
			t.Errorf("notification body for adapter %q missing %q.\n got: %s",
				tc.adapterID, want, got.Body)
		}
	}
}

// sampleHL7v2 is a minimal ADT^A01 message used to drive a single
// HL7 v2 round-trip. PATID1234 is the load-bearing identifier: any
// genuine vendor MapToFHIR implementation that uses the parsed input
// must surface this token in the resulting Bundle, which the
// vendor-parametric variants assert.
//
// MLLP framing: 0x0B <body> 0x1C 0x0D
const sampleHL7v2 = "MSH|^~\\&|SENDING_APP|SENDING_FAC|RECEIVING_APP|RECEIVING_FAC|20260618120000||ADT^A01|MSG00001|P|2.5\r" +
	"EVN|A01|20260618120000\r" +
	"PID|1||PATID1234^5^M11^ADT1^MR^GOOD HEALTH HOSPITAL~123456789^^^USSSA^SS||EVERYWOMAN^EVE^E^^^^L|JONES|19620320|F||2106-3|153 FERNWOOD DR.^^STATESVILLE^OH^35292||(206)3345232|(206)752-121||M|NON|400003403~1129086\r" +
	"PV1|1|I|2000^2012^01||||004777^GOODDOC^GOODSON^J|||SUR|||||||004777^GOODDOC^GOODSON^J|S|VisitNumber^^^Adt^VN|A|||||||||||||||||||||||||199912271408\r"

func sendHL7ToProdBinary(t *testing.T, ctx context.Context, addr, body string) {
	t.Helper()
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("dial mllp: %v", err)
	}
	defer func() { _ = conn.Close() }()

	frame := append([]byte{0x0B}, []byte(body)...)
	frame = append(frame, 0x1C, 0x0D)
	if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	// Try to read the ACK so the test waits for persistence-then-ACK
	// completion. Any read error here is non-fatal — the assertion
	// is the rest-hook receipt, not the wire-level ACK shape.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1024)
	if n, _ := conn.Read(buf); n > 0 {
		t.Logf("mllp ack: %q", buf[:n])
	}
}

// (the package already provides dumpPipelineState in dump_state_test.go;
// this file calls it via the (sub uuid.UUID) signature.)
