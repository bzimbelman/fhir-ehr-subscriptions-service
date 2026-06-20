// Fixture: production demo binary that imports an e2e/ package. This
// must surface as an F119 finding.
package main

import (
	_ "example.com/fixture/e2e/sub"
)

func main() {}
