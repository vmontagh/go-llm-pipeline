// Package imports is a Render fixture.
package imports

import (
	"fmt"
	"math/rand/v2"
	str "strings"
)

// Limit is exported.
const Limit = 3

// Pick uses a versioned import and a renamed import; fmt is used only inside
// the body, so it stays out of the snapshot.
func Pick(r *rand.Rand) *str.Builder {
	fmt.Println(r)
	return nil
}

var defaults = struct{ max int }{max: 5}

// Max selects a field of a variable, which is not a package qualifier.
var Max = defaults.max

type hidden struct{}

// Exported uses the unexported type only in an unexported field.
type Exported struct {
	Name string
	h    hidden
}
