// Fixture: test file that legitimately uses forbidden stub names to
// exercise the production wiring. The lint MUST allow these because
// they live in *_test.go.
package wiring

type defaultActivator struct{}
type noopReplayer struct{}
type stubChannelActivator struct{}

func (defaultActivator) ID() string     { return "test-default" }
func (noopReplayer) ID() string         { return "test-noop" }
func (stubChannelActivator) ID() string { return "test-stub" }

var testStubs = []Activator{
	defaultActivator{},
	stubChannelActivator{},
}

var testReplayers = []any{noopReplayer{}}
