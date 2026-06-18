// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package email implements the email notification channel: SMTP submission
// of a multipart MIME message that carries a serialized FHIR
// subscription-notification Bundle as an application/fhir+json (or +xml)
// MIME part, alongside a human-readable text/plain summary.
//
// Per docs/low-level-design/channels.md §4.3 and ADR 0010 #5, v1 ships
// SMTP-only: no S/MIME, no Direct SMTP, no DKIM signing.
package email

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"time"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/channel"
)

// Mode selects the email delivery mode.
type Mode string

// Mode values.
const (
	ModeSMTP   Mode = "smtp"
	ModeSMIME  Mode = "smime"
	ModeDirect Mode = "direct"
)

// AuthMechanism selects the SMTP AUTH mechanism.
type AuthMechanism string

// AuthMechanism values.
const (
	AuthNone    AuthMechanism = ""
	AuthPlain   AuthMechanism = "PLAIN"
	AuthLogin   AuthMechanism = "LOGIN"
	AuthCRAMMD5 AuthMechanism = "CRAM-MD5"
)

// STARTTLSPolicy controls STARTTLS upgrade behavior.
type STARTTLSPolicy string

// STARTTLSPolicy values.
const (
	STARTTLSRequired  STARTTLSPolicy = "required"
	STARTTLSPreferred STARTTLSPolicy = "preferred"
	STARTTLSDisabled  STARTTLSPolicy = "disabled"
)

// Config configures a Channel at construction time.
type Config struct {
	Mode                     Mode
	From                     string
	SubjectTemplate          string
	SMTPHost                 string
	SMTPPort                 int
	STARTTLS                 STARTTLSPolicy
	AuthMechanism            AuthMechanism
	AuthUsername             string
	AuthPassword             string
	AuthIdentity             string
	AttachmentThresholdBytes int
	RequestTimeout           time.Duration
	TLSConfig                *tls.Config
	LocalName                string
	UserAgent                string
	Metrics                  channel.MetricsEmitter
	Logger                   *slog.Logger
}

// Channel is the SMTP notification channel.
type Channel struct{}

// New builds a Channel. The stub returns ErrNotImplemented; the real
// implementation lands in the GREEN commit.
func New(_ Config) (*Channel, error) {
	return nil, errors.New("email: not implemented")
}

// Deliver is the stub; the GREEN commit replaces this with the real
// SMTP submission path.
func (c *Channel) Deliver(_ context.Context, _ channel.NotificationEnvelope) (channel.DeliveryOutcome, error) {
	return channel.DeliveryOutcome{}, errors.New("email: not implemented")
}

// Compile-time check that *Channel satisfies channel.Channel.
var _ channel.Channel = (*Channel)(nil)
