// Fixture: production wiring with a selector-form placeholder
// `var _ channel.Channel = channel.Channel(nil)` — the canonical
// real-world shape from cmd/fhir-subs/wiring.go before this lint
// surfaced it. Must surface as F49.
package pkgform

import (
	"example.com/fixture/iface_nil_placeholder/pkgform/channel"
)

var _ channel.Channel = channel.Channel(nil)
