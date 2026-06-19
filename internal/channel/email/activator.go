// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"time"
)

// ProbeOutcome is the typed result of a RCPT-TO recipient probe. It is
// distinct from DeliveryOutcome because activation has different
// terminal semantics than per-attempt delivery: an activation Probe
// surfaces three states (accepted, rejected, transient) and the API
// layer translates them to handlers.HandshakeOutcome on its own.
type ProbeOutcome int

// ProbeOutcome values.
const (
	// ProbeAccepted means the relay returned 2xx on RCPT TO. The
	// recipient mailbox is acceptable to the relay.
	ProbeAccepted ProbeOutcome = iota
	// ProbeRejected means the relay returned 5xx (or the channel's
	// configuration / endpoint shape is unrecoverable). Retrying
	// activation will not change the result.
	ProbeRejected
	// ProbeTransient means a temporary failure occurred (4xx response,
	// dial error, deadline exceeded). The operator may retry.
	ProbeTransient
)

// ProbeResult is the structured return from ProbeRecipient.
type ProbeResult struct {
	Outcome    ProbeOutcome
	Reason     string
	StatusCode int // SMTP response code; 0 for transport failures
}

// ProbeRecipient runs an RCPT-TO probe against the configured SMTP
// relay for the supplied mailto: URI. It exercises the full SMTP
// session — dial, EHLO, STARTTLS (per policy), AUTH (per policy), MAIL
// FROM, RCPT TO — and then RSET + QUIT without ever submitting DATA.
// The point is to verify the relay considers the recipient deliverable
// without actually delivering anything.
//
// This is the OP #114 (scope-reduced) implementation: a real recipient-
// existence probe replacing the `defaultActivator{}` no-op for the
// email channel type. The two outcomes that matter for the API row
// state are:
//
//   - ProbeAccepted -> handlers.HandshakeSucceeded -> row flips to "active"
//   - ProbeRejected / ProbeTransient -> handlers.HandshakeFailed -> row to "error"
//
// The activator caller (cmd/fhir-subs.emailActivator) does that
// translation; ProbeOutcome stays typed here so future operators (e.g.,
// a "requeue activation on transient" worker) can distinguish 4xx from
// 5xx without re-classifying error strings.
//
// The error return is reserved for caller-side bugs (nil channel, etc.);
// transport / protocol failures are classified into ProbeResult.
func (c *Channel) ProbeRecipient(ctx context.Context, mailtoURI string) (ProbeResult, error) {
	rcpt, err := parseMailto(mailtoURI)
	if err != nil {
		return ProbeResult{
			Outcome: ProbeRejected,
			Reason:  fmt.Sprintf("invalid mailto endpoint: %v", err),
		}, nil
	}

	attemptCtx, cancel := c.applyDeadline(ctx, time.Time{})
	defer cancel()

	client, _, err := c.dial(attemptCtx)
	if err != nil {
		return classifyDialErrorAsProbe(err), nil
	}
	defer func() {
		if cerr := client.Close(); cerr != nil && !isBenignCloseErr(cerr) {
			c.logger.WarnContext(ctx, "smtp client close error after probe",
				slog.String("channel", channelName),
				slog.String("err", cerr.Error()),
				slog.String("smtp_host", c.cfg.SMTPHost),
				slog.Int("smtp_port", c.cfg.SMTPPort))
		}
	}()

	if c.cfg.AuthMechanism != AuthNone {
		auth, authErr := buildAuth(c.cfg)
		if authErr != nil {
			return ProbeResult{
				Outcome: ProbeRejected,
				Reason:  fmt.Sprintf("auth setup: %v", authErr),
			}, nil
		}
		if cErr := client.Auth(auth); cErr != nil {
			return classifySMTPErrorAsProbe(cErr, "AUTH"), nil
		}
	}

	if mErr := client.Mail(addressOnly(c.cfg.From)); mErr != nil {
		return classifySMTPErrorAsProbe(mErr, "MAIL FROM"), nil
	}
	if rErr := client.Rcpt(rcpt); rErr != nil {
		// On RCPT failure, attempt RSET + QUIT for protocol cleanliness.
		// Stdlib's smtp.Client has no Reset(), but Quit() ends the
		// session cleanly; the deferred Close handles the rest.
		_ = client.Quit()
		return classifySMTPErrorAsProbe(rErr, "RCPT TO"), nil
	}

	// Recipient accepted. Send RSET so the relay knows we are not
	// continuing with DATA, then QUIT. RSET is best-effort; QUIT is
	// best-effort. Either failing does not change the outcome — RCPT
	// already succeeded.
	if rstErr := client.Reset(); rstErr != nil {
		c.logger.DebugContext(ctx, "smtp RSET error after successful RCPT",
			slog.String("channel", channelName),
			slog.String("err", rstErr.Error()))
	}
	_ = client.Quit()

	c.logger.InfoContext(ctx, "email rcpt-to probe accepted",
		slog.String("channel", channelName),
		slog.String("recipient", rcpt),
		slog.String("smtp_host", c.cfg.SMTPHost),
		slog.Int("smtp_port", c.cfg.SMTPPort))
	return ProbeResult{
		Outcome:    ProbeAccepted,
		Reason:     "",
		StatusCode: 250,
	}, nil
}

// classifyDialErrorAsProbe maps a connection-establishment failure to a
// ProbeResult. STARTTLS-required-but-unavailable is rejected (the
// relay's TLS posture is permanent for this configuration); everything
// else is transient.
func classifyDialErrorAsProbe(err error) ProbeResult {
	if err == nil {
		return ProbeResult{Outcome: ProbeAccepted}
	}
	msg := err.Error()
	if strings.Contains(msg, "starttls required by config") {
		return ProbeResult{
			Outcome: ProbeRejected,
			Reason:  msg,
		}
	}
	return ProbeResult{
		Outcome: ProbeTransient,
		Reason:  fmt.Sprintf("dial: %v", err),
	}
}

// classifySMTPErrorAsProbe maps an SMTP protocol error from the probe
// path to a ProbeResult. 4xx -> Transient, 5xx -> Rejected; transport
// errors (no protocol code) -> Transient.
func classifySMTPErrorAsProbe(err error, op string) ProbeResult {
	code := smtpErrorCode(err)
	msg := smtpErrorMessage(err)
	switch {
	case code == 0:
		return ProbeResult{
			Outcome: ProbeTransient,
			Reason:  fmt.Sprintf("%s: %v", op, err),
		}
	case code >= 200 && code < 300:
		return ProbeResult{Outcome: ProbeAccepted, StatusCode: code}
	case code >= 400 && code < 500:
		return ProbeResult{
			Outcome:    ProbeTransient,
			Reason:     fmt.Sprintf("%s: %d %s", op, code, msg),
			StatusCode: code,
		}
	case code >= 500 && code < 600:
		return ProbeResult{
			Outcome:    ProbeRejected,
			Reason:     fmt.Sprintf("%s: %d %s", op, code, msg),
			StatusCode: code,
		}
	default:
		return ProbeResult{
			Outcome:    ProbeTransient,
			Reason:     fmt.Sprintf("%s: unrecognized smtp code %d %s", op, code, msg),
			StatusCode: code,
		}
	}
}

// Compile-time assertion: smtp.Client carries a Reset method we depend
// on for RSET. If a future smtp version drops it, build fails here
// instead of silently degrading the probe to a no-RSET path.
var _ interface {
	Reset() error
} = (*smtp.Client)(nil)
