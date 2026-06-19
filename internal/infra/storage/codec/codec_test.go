// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package codec_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
)

func newKey(t *testing.T, b byte) []byte {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: newKey(t, 0xAA)}, 1)
	c, err := codec.New(kp)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	plaintext := []byte(`{"resourceType":"Patient","id":"abc"}`)
	aad := codec.BuildAAD("test", []byte("row-1"), 1)
	env, kv, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if kv != 1 {
		t.Errorf("expected key_version 1, got %d", kv)
	}
	if bytes.Equal(env, plaintext) {
		t.Fatal("ciphertext equal to plaintext")
	}
	got, err := c.Decrypt(env, kv, aad)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("got %q want %q", got, plaintext)
	}
}

func TestEncryptProducesUniqueNonce(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: newKey(t, 0x42)}, 1)
	c, _ := codec.New(kp)
	plaintext := []byte("repeated")
	aad := codec.BuildAAD("test", []byte("row-1"), 1)
	a, _, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two encrypts of the same plaintext produced identical ciphertext (nonce reused?)")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: newKey(t, 0x55)}, 1)
	c, _ := codec.New(kp)
	plaintext := []byte("important")
	aad := codec.BuildAAD("test", []byte("row-1"), 1)
	env, kv, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatal(err)
	}
	// flip a byte in the ciphertext (after the version + nonce header)
	env[len(env)-1] ^= 0xFF
	_, err = c.Decrypt(env, kv, aad)
	if err == nil {
		t.Fatal("expected auth-tag failure on tampered ciphertext")
	}
}

func TestUnknownKeyVersionFails(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: newKey(t, 0x77)}, 1)
	c, _ := codec.New(kp)
	plaintext := []byte("secret")
	aad := codec.BuildAAD("test", []byte("row-1"), 1)
	env, _, err := c.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decrypt(env, 99, aad)
	if err == nil {
		t.Fatal("expected error for unknown key version")
	}
	if !errors.Is(err, codec.ErrUnknownKeyVersion) {
		t.Errorf("expected ErrUnknownKeyVersion, got %v", err)
	}
}

func TestRotation(t *testing.T) {
	t.Parallel()

	keys := map[int32][]byte{1: newKey(t, 0x01), 2: newKey(t, 0x02)}
	kp := codec.NewStaticKeyProvider(keys, 2)
	c, _ := codec.New(kp)

	// New writes use version 2.
	plaintext := []byte("after-rotation")
	aadV2 := codec.BuildAAD("test", []byte("row-1"), 2)
	env, kv, err := c.Encrypt(plaintext, aadV2)
	if err != nil {
		t.Fatal(err)
	}
	if kv != 2 {
		t.Errorf("expected active version 2, got %d", kv)
	}
	got, err := c.Decrypt(env, kv, aadV2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("decrypt mismatch")
	}

	// Old envelopes encrypted under v1 still decrypt because both keys
	// are configured.
	prevProvider := codec.NewStaticKeyProvider(map[int32][]byte{1: keys[1]}, 1)
	prev, _ := codec.New(prevProvider)
	aadV1 := codec.BuildAAD("test", []byte("row-1"), 1)
	oldEnv, oldKV, err := prev.Encrypt([]byte("before-rotation"), aadV1)
	if err != nil {
		t.Fatal(err)
	}
	if oldKV != 1 {
		t.Errorf("expected v1, got %d", oldKV)
	}
	dec, err := c.Decrypt(oldEnv, oldKV, aadV1)
	if err != nil {
		t.Fatalf("rotated codec failed to read v1 envelope: %v", err)
	}
	if string(dec) != "before-rotation" {
		t.Errorf("decrypt mismatch: %q", dec)
	}
}

func TestInvalidKeySize(t *testing.T) {
	t.Parallel()

	bad := make([]byte, 16) // wrong size for AES-256
	_, err := rand.Read(bad)
	if err != nil {
		t.Fatal(err)
	}
	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: bad}, 1)
	if _, err := codec.New(kp); err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
}

func TestNoKeysFails(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{}, 1)
	if _, err := codec.New(kp); err == nil {
		t.Fatal("expected error for empty key set")
	}
}

func TestActiveVersionMustBeKnown(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: newKey(t, 1)}, 99)
	if _, err := codec.New(kp); err == nil {
		t.Fatal("expected error: active version not in keys")
	}
}

// TestSwapBetweenRowsFails proves tamper-evidence: an envelope encrypted
// against (table=A, rowKey=A) must not decrypt under (table=B, rowKey=B).
// Without AAD binding, an operator with raw DB write access could swap
// ciphertexts between rows and reads would silently succeed. With AAD
// bound to the row's identity, GCM's auth tag rejects the swap.
func TestSwapBetweenRowsFails(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: newKey(t, 0xC3)}, 1)
	c, err := codec.New(kp)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	aadA := codec.BuildAAD("ehr_events", []byte("00000000-0000-0000-0000-0000000000aa"), 1)
	aadB := codec.BuildAAD("ehr_events", []byte("00000000-0000-0000-0000-0000000000bb"), 1)

	envA, kvA, err := c.Encrypt([]byte(`{"resourceType":"Patient","id":"A"}`), aadA)
	if err != nil {
		t.Fatalf("encrypt A: %v", err)
	}
	envB, kvB, err := c.Encrypt([]byte(`{"resourceType":"Patient","id":"B"}`), aadB)
	if err != nil {
		t.Fatalf("encrypt B: %v", err)
	}

	if _, err := c.Decrypt(envA, kvA, aadA); err != nil {
		t.Fatalf("legit decrypt A: %v", err)
	}
	if _, err := c.Decrypt(envB, kvB, aadB); err != nil {
		t.Fatalf("legit decrypt B: %v", err)
	}

	// Operator swaps envelope bytes between rows. Reading row B's slot
	// (which now holds envA) under row B's AAD must fail.
	if _, err := c.Decrypt(envA, kvA, aadB); err == nil {
		t.Fatal("expected swap detection: envA decrypted under aadB")
	}
	if _, err := c.Decrypt(envB, kvB, aadA); err == nil {
		t.Fatal("expected swap detection: envB decrypted under aadA")
	}
}

// TestSwapBetweenTablesFails proves table-level binding: two rows with
// the SAME row key but in different tables must not be interchangeable.
func TestSwapBetweenTablesFails(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: newKey(t, 0xD4)}, 1)
	c, _ := codec.New(kp)

	rowKey := []byte("same-id-different-table")
	aadEvents := codec.BuildAAD("ehr_events", rowKey, 1)
	aadChanges := codec.BuildAAD("resource_changes", rowKey, 1)

	envEvents, kv, err := c.Encrypt([]byte("payload"), aadEvents)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decrypt(envEvents, kv, aadChanges); err == nil {
		t.Fatal("expected cross-table swap detection")
	}
}

// TestKeyVersionAADBinding ensures the key_version is bound: writing an
// envelope under key_version 1 must not verify if Decrypt is told the
// row's key_version is 2 (even if both keys exist).
func TestKeyVersionAADBinding(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{
		1: newKey(t, 0x11),
		2: newKey(t, 0x22),
	}, 1)
	c, _ := codec.New(kp)

	rowKey := []byte("row-1")
	aadV1 := codec.BuildAAD("ehr_events", rowKey, 1)
	aadV2 := codec.BuildAAD("ehr_events", rowKey, 2)

	env, kv, err := c.Encrypt([]byte("payload"), aadV1)
	if err != nil {
		t.Fatal(err)
	}
	if kv != 1 {
		t.Fatalf("expected kv=1, got %d", kv)
	}
	// Caller mistakenly passes the wrong key_version AAD even though
	// they passed the right key_version for cipher selection.
	if _, err := c.Decrypt(env, kv, aadV2); err == nil {
		t.Fatal("expected AAD mismatch failure when key_version in AAD differs")
	}
}
