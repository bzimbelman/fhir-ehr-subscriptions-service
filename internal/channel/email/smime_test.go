// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

type fakeSigner struct {
	called int
	err    error
	prefix []byte
}

func (f *fakeSigner) Sign(message []byte) ([]byte, error) {
	f.called++
	if f.err != nil {
		return nil, f.err
	}
	// A token-multipart/signed wrapper just to satisfy the channel's
	// post-condition without pulling in real PKCS#7 here.
	out := append([]byte{}, f.prefix...)
	out = append(out, "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"\r\n\r\n"...)
	out = append(out, message...)
	return out, nil
}

// P2.3: New rejects ModeSMIME without a Signer.
func TestNewSMIME_RequiresSigner(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{
		Mode:     email.ModeSMIME,
		From:     "fhir-subs@example.org",
		SMTPHost: "smtp.example.org",
		SMTPPort: 587,
	})
	if !errors.Is(err, email.ErrSignerRequired) {
		t.Fatalf("want ErrSignerRequired, got %v", err)
	}
}

// P2.3: New accepts ModeSMIME when a Signer is wired.
func TestNewSMIME_AcceptsWithSigner(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{
		Mode:     email.ModeSMIME,
		From:     "fhir-subs@example.org",
		SMTPHost: "smtp.example.org",
		SMTPPort: 587,
		Signer:   &fakeSigner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
}

// P2.3: New still rejects ModeDirect (deferred entirely; no Signer
// adapter — Direct messaging is its own protocol stack post-MVP).
func TestNewDirect_StillRejected(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{
		Mode:     email.ModeDirect,
		From:     "fhir-subs@example.org",
		SMTPHost: "smtp.example.org",
		SMTPPort: 587,
	})
	if err == nil {
		t.Fatalf("expected error: ModeDirect not supported")
	}
	if !strings.Contains(err.Error(), "direct") {
		t.Errorf("error did not mention direct: %v", err)
	}
}

// P2.3: New rejects an unknown mode.
func TestNewUnknownMode_Rejected(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{
		Mode:     "fancy",
		From:     "fhir-subs@example.org",
		SMTPHost: "smtp.example.org",
		SMTPPort: 587,
	})
	if err == nil {
		t.Fatalf("expected error on unknown mode")
	}
}
