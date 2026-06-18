// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email

import (
	"bytes"
	"errors"
	"fmt"
)

// Signer is the SPI for S/MIME signing of outbound MIME messages
// (P2.3 MVP). Implementations wrap the signing identity (private
// key + certificate chain) and produce a `multipart/signed; protocol="application/pkcs7-signature"`
// MIME envelope around the unsigned message bytes.
//
// The concrete implementation is deliberately not provided in this
// batch — a production-grade S/MIME signer requires a careful
// PKCS#7 SignedData encoding, certificate-store integration, and
// trust-bundle validation that is post-MVP. Operators that need
// S/MIME today implement this SPI themselves (typically against
// `github.com/digitorus/pkcs7` or similar) and inject it via
// Config.Signer.
//
// Sign accepts the full unsigned RFC 5322 message bytes (including
// headers) and returns a new MIME message that wraps the original
// plus a detached PKCS#7 signature. The returned bytes replace the
// unsigned message that the channel would otherwise have sent.
type Signer interface {
	// Sign signs the given MIME-encoded message bytes and returns
	// the multipart/signed envelope. Implementations must preserve
	// the From / To / Subject headers in the outer envelope so the
	// SMTP relay can route the message.
	Sign(message []byte) ([]byte, error)
}

// ErrSignerRequired is returned by New when Mode=ModeSMIME but
// Config.Signer is nil. The message names the field so an operator
// can fix it without reading the source.
var ErrSignerRequired = errors.New("email: Mode=smime requires Config.Signer to be set")

// noopSigner is a defensive default that fails any Sign call. The
// channel only uses it as a sentinel; New rejects ModeSMIME with no
// Signer at startup, so this is never reached in production.
type noopSigner struct{}

func (noopSigner) Sign([]byte) ([]byte, error) {
	return nil, errors.New("email: no S/MIME signer configured")
}

// applySMIMESignature wraps the unsigned MIME message in the
// configured signer's envelope. Returns the original bytes when no
// signer is wired (callers ensure this only happens for ModeSMTP).
func (c *Channel) applySMIMESignature(message []byte) ([]byte, error) {
	if c.cfg.Mode != ModeSMIME {
		return message, nil
	}
	if c.cfg.Signer == nil {
		return nil, ErrSignerRequired
	}
	signed, err := c.cfg.Signer.Sign(message)
	if err != nil {
		return nil, fmt.Errorf("email: S/MIME sign: %w", err)
	}
	if !bytes.Contains(signed, []byte("Content-Type: multipart/signed")) {
		return nil, errors.New("email: S/MIME signer must produce a multipart/signed envelope")
	}
	return signed, nil
}
