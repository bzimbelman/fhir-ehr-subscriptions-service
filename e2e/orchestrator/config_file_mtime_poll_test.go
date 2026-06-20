// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2E_ProdBinary_SecretFileMtimeTriggersReload (story #152) asserts
// that the production binary watches every ${file:...}-interpolated
// path and re-reads the config when one rotates. Pre-fix, production
// never started a watcher — Vault Agent / cert-manager rotation went
// unobserved because the operator has no signaling rights into the
// pod.
//
// Mechanic: render codec.keys[0].material as a ${file:/path/to/key}
// reference, mutate the file on disk, assert a "config reload applied"
// line emerges with trigger=file_mtime within the polling window. No
// signal sent — that's the whole point: rotation without signaling
// rights into the pod.
//
// This test replaces the prior B-35 self-installed handler. It now
// exercises the full production wiring.
func TestE2E_ProdBinary_SecretFileMtimeTriggersReload(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "encryption-key.b64")
	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(0xA0 + i)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(rawKey)), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:            h.DBURL,
		FacilityID:             "e2e-prod-mtime",
		AdapterID:              "default",
		Insecure:               true,
		GracePeriod:            5 * time.Second,
		AuthAudience:           "https://api.test.local",
		AuthAllowInsecureJWKS:  true,
		CodecKeyMaterialFile:   keyPath,
		SecretFilePollInterval: 50 * time.Millisecond,
	})
	defer bin.Stop(t, 5*time.Second)

	// Snapshot the line count before rotation so we look only at log
	// lines emitted AFTER the rotation.
	preCount := len(bin.Stderr().Lines())

	// Rotate the secret. The rotated key is structurally valid so the
	// reload should be applied (codec key rotation IS a hot-reloadable
	// subset per AC).
	rotated := make([]byte, 32)
	for i := range rotated {
		rotated[i] = byte(0xC0 + i)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(rotated)), 0o600); err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(keyPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Watcher polls every 50ms; allow generous slack.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lines := bin.Stderr().Lines()
		if len(lines) > preCount {
			for _, l := range lines[preCount:] {
				if strings.Contains(l, "config reload applied") &&
					strings.Contains(l, "file_mtime") {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	for i, l := range bin.Stderr().Lines() {
		if i < preCount {
			continue
		}
		t.Logf("post-rotation captured: %s", l)
	}
	t.Fatalf("secret-file rotation did not trigger a 'config reload applied' line with trigger=file_mtime — production never started the secret-file watcher")
}
