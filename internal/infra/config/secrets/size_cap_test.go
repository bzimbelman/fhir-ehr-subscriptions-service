// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package secrets_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/secrets"
)

// S-15 #4: ${file:...} reads are capped at MaxSecretFileSize so a path
// pointed at /dev/zero cannot OOM the loader.
func TestResolve_FilePlaceholder_RejectsOversize(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bigPath := filepath.Join(dir, "huge.bin")
	// Write MaxSecretFileSize+10 bytes.
	if err := os.WriteFile(bigPath, make([]byte, secrets.MaxSecretFileSize+10), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	tree := map[string]interface{}{
		"some_token": "${file:" + bigPath + "}",
	}
	_, _, _, err := secrets.ResolveWithFilePaths(tree, redaction.NewMap())
	if err == nil {
		t.Fatal("expected ErrSecretFileTooLarge; got nil")
	}
	if !errors.Is(err, secrets.ErrSecretFileTooLarge) {
		t.Fatalf("expected ErrSecretFileTooLarge; got %v", err)
	}
}

// Verify the at-the-limit case succeeds — the cap is inclusive.
func TestResolve_FilePlaceholder_AtLimitSucceeds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	smallPath := filepath.Join(dir, "small.txt")
	// Stay below the limit by a healthy margin so we're not testing
	// fencepost behaviour against real disk pressure.
	if err := os.WriteFile(smallPath, []byte("hello-secret"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	tree := map[string]interface{}{
		"token": "${file:" + smallPath + "}",
	}
	out, rmap, _, err := secrets.ResolveWithFilePaths(tree, redaction.NewMap())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if out["token"] != "hello-secret" {
		t.Fatalf("token: %v", out["token"])
	}
	if !rmap.IsSensitive("token") {
		t.Fatal("expected token to be tagged sensitive")
	}
}
