// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
)

// N-1: SubjectTemplate is sanitized for CRLF before being written so a
// future template-substitution change cannot regress into a header
// injection. Today the template is used verbatim, but mishandling CRLF
// is a one-line away from a security incident; defense in depth.
func TestN1_BuildMIMEStripsCRLFFromSubject(t *testing.T) {
	t.Parallel()
	c := &Channel{cfg: Config{
		SubjectTemplate: "Hello\r\nBcc: attacker@example.com\r\n",
		From:            "from@example.com",
		UserAgent:       "test-mailer",
		AttachmentThresholdBytes: 1 << 20,
	}}
	env := channel.NotificationEnvelope{
		SubscriptionID: uuid.New(),
		BundleBytes:    []byte(`{"resourceType":"Bundle","entry":[]}`),
		BundleKind:     channel.BundleEventNotification,
		ContentType:    channel.ContentTypeFHIRJSON,
	}
	out, err := c.buildMIME(env, "rcpt@example.com")
	if err != nil {
		t.Fatalf("buildMIME: %v", err)
	}
	mime := string(out)
	headerEnd := strings.Index(mime, "\r\n\r\n")
	if headerEnd < 0 {
		t.Fatalf("no header/body separator in MIME output:\n%s", mime)
	}
	headers := mime[:headerEnd]
	// Each line in the headers block is ONE header. With CRLF stripped,
	// the malicious "Bcc:" payload collapses into the Subject value. The
	// failure mode N-1 guards against is "Bcc:" appearing as the start
	// of a new header line.
	for _, line := range strings.Split(headers, "\r\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "bcc:") {
			t.Fatalf("CRLF injection: Bcc became its own header: %q", line)
		}
	}
}
