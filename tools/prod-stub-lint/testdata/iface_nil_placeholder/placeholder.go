// Fixture: production package with a self-converting nil interface
// placeholder (Finding #49). The line `var _ Channel = Channel(nil)`
// is the canonical broken pattern — a placeholder admitting the
// production binary cannot supply a real Channel.
//
// The neighboring `var _ Channel = (*RealChannel)(nil)` is a
// legitimate compile-time interface assertion, NOT a placeholder, and
// MUST be allowed by the lint.
package placeholder

type Channel interface {
	Send([]byte) error
}

type RealChannel struct{}

func (*RealChannel) Send([]byte) error { return nil }

// Forbidden — Finding #49 placeholder.
var _ Channel = Channel(nil)

// Allowed — concrete-type interface assertion.
var _ Channel = (*RealChannel)(nil)
