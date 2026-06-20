// Fixture: production internal package with no e2e/ imports. Must NOT
// surface a finding.
package clean

import (
	"fmt"
)

func Greet() string { return fmt.Sprintf("hello") }
