// Copyright the fhir-subscriptions-foss authors.
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
	env, kv, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if kv != 1 {
		t.Errorf("expected key_version 1, got %d", kv)
	}
	if bytes.Equal(env, plaintext) {
		t.Fatal("ciphertext equal to plaintext")
	}
	got, err := c.Decrypt(env, kv)
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
	a, _, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := c.Encrypt(plaintext)
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
	env, kv, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	// flip a byte in the ciphertext (after the version + nonce header)
	env[len(env)-1] ^= 0xFF
	_, err = c.Decrypt(env, kv)
	if err == nil {
		t.Fatal("expected auth-tag failure on tampered ciphertext")
	}
}

func TestUnknownKeyVersionFails(t *testing.T) {
	t.Parallel()

	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: newKey(t, 0x77)}, 1)
	c, _ := codec.New(kp)
	plaintext := []byte("secret")
	env, _, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decrypt(env, 99)
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
	env, kv, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if kv != 2 {
		t.Errorf("expected active version 2, got %d", kv)
	}
	got, err := c.Decrypt(env, kv)
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
	oldEnv, oldKV, err := prev.Encrypt([]byte("before-rotation"))
	if err != nil {
		t.Fatal(err)
	}
	if oldKV != 1 {
		t.Errorf("expected v1, got %d", oldKV)
	}
	dec, err := c.Decrypt(oldEnv, oldKV)
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
