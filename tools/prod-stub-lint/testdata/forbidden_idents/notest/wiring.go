// Fixture: production code that registers forbidden no-op stub
// identifiers. Each call below must surface as an F50 finding.
package wiring

type defaultActivator struct{}
type noopReplayer struct{}
type stubChannelActivator struct{}

type Activator interface{ ID() string }
type Replayer interface{ ID() string }

func (defaultActivator) ID() string     { return "default" }
func (noopReplayer) ID() string         { return "noop" }
func (stubChannelActivator) ID() string { return "stub" }

var Activators = map[string]Activator{
	"websocket": defaultActivator{},
	"message":   defaultActivator{},
	"stub":      stubChannelActivator{},
}

var Replayers = map[string]Replayer{
	"default": noopReplayer{},
}
