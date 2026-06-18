// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package codec implements column-level AES-256-GCM encryption for
// PHI-bearing columns. The envelope is a single bytea column with the
// shape:
//
//	[version u8][nonce 12]B[ciphertext+tag]
//
// On read, the storage layer also has the row's `key_version int4`
// column which is asserted equal to the envelope's version byte.
package codec

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// ErrUnknownKeyVersion is returned when an envelope's key_version is
// not in the configured set.
var ErrUnknownKeyVersion = errors.New("codec: unknown key version")

// ErrEnvelopeMalformed is returned when the bytes do not look like an
// envelope produced by this codec.
var ErrEnvelopeMalformed = errors.New("codec: envelope malformed")

// ErrKeyVersionMismatch is returned when the key_version stored on the
// row disagrees with the envelope header.
var ErrKeyVersionMismatch = errors.New("codec: key_version mismatch between column and envelope")

// KeyProvider hands out AES-256 keys by version.
type KeyProvider interface {
	// ActiveVersion is the version new writes use.
	ActiveVersion() int32
	// KeyFor returns the 32-byte AES-256 key for the given version,
	// or ErrUnknownKeyVersion if the version is not configured.
	KeyFor(version int32) ([]byte, error)
	// Versions returns all configured versions.
	Versions() []int32
}

// StaticKeyProvider is a fixed map. Production typically wraps a KMS or
// secret-store loader behind this same interface.
type StaticKeyProvider struct {
	keys   map[int32][]byte
	active int32
}

// NewStaticKeyProvider returns a key provider backed by the given map
// and active version. Validation (key length, active-version-known) is
// performed by codec.New, not here.
func NewStaticKeyProvider(keys map[int32][]byte, active int32) *StaticKeyProvider {
	cp := make(map[int32][]byte, len(keys))
	for k, v := range keys {
		b := make([]byte, len(v))
		copy(b, v)
		cp[k] = b
	}
	return &StaticKeyProvider{keys: cp, active: active}
}

// ActiveVersion implements KeyProvider.
func (p *StaticKeyProvider) ActiveVersion() int32 { return p.active }

// KeyFor implements KeyProvider.
func (p *StaticKeyProvider) KeyFor(v int32) ([]byte, error) {
	k, ok := p.keys[v]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownKeyVersion, v)
	}
	out := make([]byte, len(k))
	copy(out, k)
	return out, nil
}

// Versions implements KeyProvider.
func (p *StaticKeyProvider) Versions() []int32 {
	out := make([]int32, 0, len(p.keys))
	for v := range p.keys {
		out = append(out, v)
	}
	return out
}

// Codec is the AES-256-GCM encryption codec for PHI columns.
type Codec struct {
	kp     KeyProvider
	gcms   map[int32]cipher.AEAD
	active int32
}

// New constructs a Codec from a KeyProvider. All configured keys are
// loaded into AES-GCM ciphers eagerly so a misconfiguration fails at
// startup.
func New(kp KeyProvider) (*Codec, error) {
	versions := kp.Versions()
	if len(versions) == 0 {
		return nil, errors.New("codec: no keys configured")
	}
	gcms := make(map[int32]cipher.AEAD, len(versions))
	for _, v := range versions {
		key, err := kp.KeyFor(v)
		if err != nil {
			return nil, err
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("codec: key for version %d is %d bytes; expected 32", v, len(key))
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("codec: aes for version %d: %w", v, err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("codec: gcm for version %d: %w", v, err)
		}
		gcms[v] = gcm
	}
	active := kp.ActiveVersion()
	if _, ok := gcms[active]; !ok {
		return nil, fmt.Errorf("codec: active version %d not in configured keys", active)
	}
	return &Codec{kp: kp, gcms: gcms, active: active}, nil
}

// envelopeVersionByte is reserved to allow future format changes.
const envelopeFormat byte = 0x01

// Encrypt produces the ciphertext envelope plus the key_version column
// that should accompany it on the row.
//
// Envelope layout:
//
//	[1 byte format][1 byte zero pad][2 bytes nonce_len][nonce][ciphertext]
//
// We keep the layout self-describing so a future codec can recognize
// foreign-shaped envelopes deterministically.
func (c *Codec) Encrypt(plaintext []byte) ([]byte, int32, error) {
	gcm, ok := c.gcms[c.active]
	if !ok {
		return nil, 0, fmt.Errorf("%w: active=%d", ErrUnknownKeyVersion, c.active)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, 0, fmt.Errorf("codec: read nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)

	// Layout: [format][reserved=0][nonceLen hi][nonceLen lo][nonce][ct]
	out := make([]byte, 0, 4+len(nonce)+len(ct))
	out = append(out, envelopeFormat, 0x00)
	nl := len(nonce)
	out = append(out, byte(nl>>8), byte(nl&0xFF))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, c.active, nil
}

// Decrypt unwraps an envelope produced by this codec. The keyVersion
// argument is the value of the row's key_version column; we use it to
// pick the cipher.
func (c *Codec) Decrypt(envelope []byte, keyVersion int32) ([]byte, error) {
	if len(envelope) < 4 {
		return nil, ErrEnvelopeMalformed
	}
	if envelope[0] != envelopeFormat {
		return nil, fmt.Errorf("%w: bad format byte 0x%02x", ErrEnvelopeMalformed, envelope[0])
	}
	nl := int(envelope[2])<<8 | int(envelope[3])
	if 4+nl > len(envelope) {
		return nil, ErrEnvelopeMalformed
	}
	nonce := envelope[4 : 4+nl]
	ct := envelope[4+nl:]

	gcm, ok := c.gcms[keyVersion]
	if !ok {
		return nil, fmt.Errorf("%w: requested=%d", ErrUnknownKeyVersion, keyVersion)
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("codec: open: %w", err)
	}
	return pt, nil
}

// ActiveVersion is the key version new writes will use.
func (c *Codec) ActiveVersion() int32 { return c.active }
