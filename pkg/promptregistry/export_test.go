package promptregistry

// IsMissingKeyError exposes the unexported classifier for white-box regression
// tests that construct a synthetic [text/template.ExecError]. The public Render
// API cannot reproduce a bare template name (names are full file paths), so a
// collision between the name and a cause payload is only reachable here.
func IsMissingKeyError(err error) bool { return isMissingKeyError(err) }

// UpdateEnv exposes the golden-update environment variable name so black-box
// tests can exercise its interaction with WithUpdate.
const UpdateEnv = updateEnv
