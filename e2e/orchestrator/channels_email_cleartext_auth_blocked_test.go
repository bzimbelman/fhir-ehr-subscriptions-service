// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// TestE2E_Email_CleartextAuth_NewRefusesConstruction verifies B-15:
// configuring the email channel with STARTTLS=Disabled AND a non-empty
// AuthMechanism (and no AllowCleartextAuth opt-in) is refused at
// construction time. Operators on a closed-network relay can still opt
// in by setting AllowCleartextAuth=true.
func TestE2E_Email_CleartextAuth_NewRefusesConstruction(t *testing.T) {
	t.Parallel()

	cfg := email.Config{
		From:          "noreply@example.org",
		SMTPHost:      "smtp.example.org",
		SMTPPort:      25,
		STARTTLS:      email.STARTTLSDisabled,
		AuthMechanism: email.AuthPlain,
		AuthUsername:  "ops",
		AuthPassword:  "s3cret",
	}
	if _, err := email.New(cfg); err == nil {
		t.Fatalf("email.New must refuse PLAIN AUTH over plaintext (B-15)")
	} else if !strings.Contains(err.Error(), "AllowCleartextAuth") {
		t.Errorf("error should reference AllowCleartextAuth opt-in; got %v", err)
	}

	// Explicit opt-in succeeds.
	cfg.AllowCleartextAuth = true
	if _, err := email.New(cfg); err != nil {
		t.Fatalf("email.New must allow cleartext AUTH when AllowCleartextAuth=true; got %v", err)
	}
}
