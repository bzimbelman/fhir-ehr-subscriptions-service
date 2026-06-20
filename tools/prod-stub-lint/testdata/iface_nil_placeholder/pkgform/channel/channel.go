// Fixture: a channel package whose Channel interface is referenced
// from a sibling package via a selector expression.
package channel

type Channel interface {
	Send([]byte) error
}
