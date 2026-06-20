// Fixture: clean production binary. Lint MUST report zero findings.
package main

import "fmt"

type Activator interface{ ID() string }

type httpActivator struct{ name string }

func (h httpActivator) ID() string { return h.name }

var _ Activator = (*httpActivator)(nil)

var Activators = map[string]Activator{
	"rest-hook": httpActivator{name: "rest-hook"},
}

func main() { fmt.Println(Activators) }
