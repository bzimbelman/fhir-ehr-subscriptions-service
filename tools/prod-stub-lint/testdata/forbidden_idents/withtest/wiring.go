// Fixture: production wiring file with a clean implementation.
// Forbidden stubs only appear in the *_test.go neighbor file.
package wiring

type realActivator struct{ name string }

func (r realActivator) ID() string { return r.name }

type Activator interface{ ID() string }

var Activators = map[string]Activator{
	"rest-hook": realActivator{name: "rest-hook"},
}
