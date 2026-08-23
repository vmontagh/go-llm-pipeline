package promptregistry

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"testing"
)

// goldenExt is the file extension for golden fixture files.
const goldenExt = ".golden"

// updateEnv, when set to a non-empty value, makes AssertGolden rewrite golden
// files instead of comparing against them. It offers the same effect as
// WithUpdate without threading a flag through the call site.
const updateEnv = "PROMPTREGISTRY_UPDATE_GOLDEN"

// GoldenCase pairs a template reference with the fixture data used to render it
// during a golden test.
type GoldenCase struct {
	// Ref identifies the template to render.
	Ref Ref
	// Data is the input passed to Render for this case.
	Data any
}

// goldenConfig holds the settings applied by AssertGolden.
type goldenConfig struct {
	update bool
}

// GoldenOption customizes AssertGolden.
type GoldenOption func(*goldenConfig)

// WithUpdate controls whether AssertGolden rewrites golden files instead of
// comparing against them. It takes precedence only when true; the
// PROMPTREGISTRY_UPDATE_GOLDEN environment variable can enable updating without
// changing the call site.
func WithUpdate(update bool) GoldenOption {
	return func(c *goldenConfig) {
		c.update = update
	}
}

// AssertGolden renders every template in r against the supplied cases and
// compares each result with the golden file dir/<name>/<version>.golden. A
// mismatch, a missing case for a registered template, a case naming an
// unregistered template, or a render error fails the test. When updating is
// enabled (via WithUpdate or the PROMPTREGISTRY_UPDATE_GOLDEN environment
// variable) the golden files are rewritten from the rendered output instead.
//
// It gives downstream packages a single call to make prompt changes a
// reviewable, testable diff.
func AssertGolden(t testing.TB, r *Registry, dir string, cases []GoldenCase, opts ...GoldenOption) {
	t.Helper()

	cfg := goldenConfig{update: os.Getenv(updateEnv) != ""}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	byRef := make(map[Ref]any, len(cases))
	for _, c := range cases {
		if _, ok := r.templates[c.Ref]; !ok {
			t.Errorf("AssertGolden: case %s/%s is not registered", c.Ref.Name, c.Ref.Version)
			continue
		}
		if _, ok := byRef[c.Ref]; ok {
			t.Errorf("AssertGolden: duplicate case for %s/%s", c.Ref.Name, c.Ref.Version)
			continue
		}
		byRef[c.Ref] = c.Data
	}

	for _, ref := range r.List() {
		data, ok := byRef[ref]
		if !ok {
			t.Errorf("AssertGolden: no case for registered template %s/%s", ref.Name, ref.Version)
			continue
		}
		got, err := r.Render(t.Context(), ref.Name, ref.Version, data)
		if err != nil {
			t.Errorf("Render(%s/%s) error: %v", ref.Name, ref.Version, err)
			continue
		}
		file, err := goldenFilePath(dir, ref)
		if err != nil {
			t.Errorf("AssertGolden: %v", err)
			continue
		}
		if cfg.update {
			if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
				t.Fatalf("AssertGolden: creating %q: %v", filepath.Dir(file), err)
			}
			if err := os.WriteFile(file, []byte(got), 0o600); err != nil {
				t.Fatalf("AssertGolden: writing %q: %v", file, err)
			}
			continue
		}
		wantBytes, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("AssertGolden: reading golden %q: %v (run with %s=1 to create it)", file, err, updateEnv)
			continue
		}
		if want := string(wantBytes); got != want {
			t.Errorf("Render(%s/%s) golden mismatch:\n want: %q\n got:  %q", ref.Name, ref.Version, want, got)
		}
	}
}

// goldenFilePath builds the golden file path for ref. It preserves the
// template's directory hierarchy (<dir>/<name>/<version>.golden) so distinct
// names cannot collide onto a single file, and localizes the slash-separated
// ref components so a name or version carrying a path separator (valid in an
// io/fs path but not on the host filesystem) cannot escape dir.
func goldenFilePath(dir string, ref Ref) (string, error) {
	rel := path.Join(ref.Name, ref.Version+goldenExt)
	local, err := filepath.Localize(rel)
	if err != nil {
		return "", fmt.Errorf("%w: golden path for %s/%s: %w", ErrInvalidTemplate, ref.Name, ref.Version, err)
	}
	return filepath.Join(dir, local), nil
}
