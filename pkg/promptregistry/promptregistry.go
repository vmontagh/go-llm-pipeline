// Package promptregistry treats LLM prompts as first-class, versioned, testable
// assets instead of inline string literals scattered across caller code.
//
// A [Registry] loads prompt templates from an [fs.FS] (typically an
// [embed.FS]), each addressable by a stable (name, version) pair such as
// ("classify_asset", "v3"). Explicit versions let a prompt evolve without
// silently changing behavior for callers pinned to an older version.
//
// Templates are ordinary [text/template] sources stored as reviewable .tmpl
// files laid out as <name>/<version>.tmpl within the supplied filesystem, so a
// prompt change is a self-contained, diffable, testable artifact. Rendering
// runs fully offline with no network or database access.
//
// Rendering fails closed: the "missingkey=error" template option is always
// applied, so a reference to a field or key the data does not provide surfaces
// as [ErrMissingField] rather than an empty span. Every failure category is a
// sentinel ([ErrTemplateNotFound], [ErrMissingField], [ErrRenderFailed],
// [ErrInvalidTemplate]) returned wrapped with %w so callers can classify it
// with [errors.Is].
//
// A Registry is immutable after construction, so [Registry.Render] is safe for
// concurrent use by multiple goroutines.
package promptregistry

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"text/template"
)

// Sentinel errors returned by the package, wrapped with %w so callers classify
// a failure with errors.Is rather than by inspecting error text.
var (
	// ErrTemplateNotFound is returned by Render when no template is registered
	// for the requested (name, version) pair.
	ErrTemplateNotFound = errors.New("template not found")
	// ErrMissingField is returned by Render when the data does not provide a
	// field or key the template references. Rendering fails closed via the
	// text/template "missingkey=error" option so a typo surfaces here instead
	// of producing an empty span.
	ErrMissingField = errors.New("missing field")
	// ErrRenderFailed is returned by Render for any other template execution
	// failure, such as a method or function call that itself returns an error.
	ErrRenderFailed = errors.New("render failed")
	// ErrInvalidTemplate is returned by New when the source filesystem has a
	// malformed layout or contains a template that fails to parse.
	ErrInvalidTemplate = errors.New("invalid template")
)

// templateExt is the file extension identifying a prompt template within the
// source filesystem.
const templateExt = ".tmpl"

// Ref identifies a single registered template by its stable name and version.
type Ref struct {
	// Name is the template name, taken from the parent directory of the .tmpl
	// file (for example "classify_asset").
	Name string
	// Version is the template version, taken from the file name without its
	// extension (for example "v3").
	Version string
}

// Option configures a Registry at construction time.
type Option func(*config)

// config holds the settings applied by New before any template is parsed.
type config struct {
	root  string
	funcs template.FuncMap
}

// WithRoot restricts New to a sub-directory of the supplied filesystem. It is
// convenient when prompts live under a fixed prefix such as "templates" within
// a larger embed.FS. The default root is the filesystem root.
func WithRoot(dir string) Option {
	return func(c *config) {
		c.root = dir
	}
}

// WithFuncs makes the given functions available to every template in the
// registry, mirroring [text/template.Template.Funcs]. Later options merge into
// earlier ones; a duplicate name overrides the prior function.
func WithFuncs(funcs template.FuncMap) Option {
	return func(c *config) {
		if c.funcs == nil {
			c.funcs = make(template.FuncMap, len(funcs))
		}
		for name, fn := range funcs {
			c.funcs[name] = fn
		}
	}
}

// Registry holds named, versioned prompt templates parsed once from a source
// filesystem. It is immutable after construction and safe for concurrent use.
// The zero value is not usable; construct one with New.
type Registry struct {
	templates map[Ref]*template.Template
	refs      []Ref
	names     []string
}

// New parses every <name>/<version>.tmpl file in fsys into a Registry. The
// parent directory of each file becomes the template name and the file name
// without its .tmpl extension becomes the version. Files that do not end in
// .tmpl are ignored; a .tmpl file that is not nested under a name directory, or
// that fails to parse, yields ErrInvalidTemplate.
func New(fsys fs.FS, opts ...Option) (*Registry, error) {
	if fsys == nil {
		return nil, fmt.Errorf("%w: nil filesystem", ErrInvalidTemplate)
	}
	cfg := config{root: "."}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.root != "." {
		sub, err := fs.Sub(fsys, cfg.root)
		if err != nil {
			return nil, fmt.Errorf("%w: root %q: %w", ErrInvalidTemplate, cfg.root, err)
		}
		fsys = sub
	}

	templates := make(map[Ref]*template.Template)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, templateExt) {
			return nil
		}
		ref, err := refFromPath(p)
		if err != nil {
			return err
		}
		if _, ok := templates[ref]; ok {
			return fmt.Errorf("%w: duplicate template %s/%s", ErrInvalidTemplate, ref.Name, ref.Version)
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("%w: reading %q: %w", ErrInvalidTemplate, p, err)
		}
		tmpl, err := template.New(p).Option("missingkey=error").Funcs(cfg.funcs).Parse(string(src))
		if err != nil {
			return fmt.Errorf("%w: parsing %q: %w", ErrInvalidTemplate, p, err)
		}
		templates[ref] = tmpl
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalidTemplate) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: walking filesystem: %w", ErrInvalidTemplate, err)
	}
	if len(templates) == 0 {
		return nil, fmt.Errorf("%w: no templates found", ErrInvalidTemplate)
	}

	return &Registry{
		templates: templates,
		refs:      sortedRefs(templates),
		names:     uniqueNames(templates),
	}, nil
}

// refFromPath derives a Ref from a template path of the form
// <name>/<version>.tmpl. A template placed directly at the filesystem root has
// no name directory and is rejected with ErrInvalidTemplate.
func refFromPath(p string) (Ref, error) {
	name := path.Dir(p)
	if name == "." {
		return Ref{}, fmt.Errorf("%w: %q must be nested under a name directory", ErrInvalidTemplate, p)
	}
	version := strings.TrimSuffix(path.Base(p), templateExt)
	return Ref{Name: name, Version: version}, nil
}

// Render executes the template registered for (name, version) against data and
// returns the fully rendered string. It returns ErrTemplateNotFound when no
// such template exists, ErrMissingField when data omits a referenced field or
// key, and ErrRenderFailed for any other execution failure. The context is
// honored for cancellation but not stored; it leaves room for a future remote
// loader without changing the signature.
func (r *Registry) Render(ctx context.Context, name, version string, data any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("promptregistry: %w", err)
	}
	tmpl, ok := r.templates[Ref{Name: name, Version: version}]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s", ErrTemplateNotFound, name, version)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		if isMissingKeyError(err) {
			return "", fmt.Errorf("%w: %s/%s: %w", ErrMissingField, name, version, err)
		}
		return "", fmt.Errorf("%w: %s/%s: %w", ErrRenderFailed, name, version, err)
	}
	return buf.String(), nil
}

// missingKeyMarkers are the substrings text/template uses to report a
// missing map key or struct field under the "missingkey=error" option. The
// package inspects the message because text/template does not expose a typed
// sentinel for this boundary condition; the strings belong to the standard
// library, not to this package's own error contract.
var missingKeyMarkers = []string{
	"map has no entry for key",
	"can't evaluate field",
	"nil data; no entry for key",
}

// isMissingKeyError reports whether err is a text/template execution failure
// caused by a missing map key or struct field.
func isMissingKeyError(err error) bool {
	msg := err.Error()
	for _, marker := range missingKeyMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// Names returns the distinct template names in the registry, sorted
// lexicographically. The returned slice is a copy the caller may modify.
func (r *Registry) Names() []string {
	return slices.Clone(r.names)
}

// List returns every registered template as a Ref, sorted by name and then by
// version. The returned slice is a copy the caller may modify. It lets callers
// (such as the golden-test helper) enumerate the full set of templates.
func (r *Registry) List() []Ref {
	return slices.Clone(r.refs)
}

// sortedRefs returns the keys of templates sorted by name then version.
func sortedRefs(templates map[Ref]*template.Template) []Ref {
	refs := make([]Ref, 0, len(templates))
	for ref := range templates {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b Ref) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	return refs
}

// uniqueNames returns the distinct template names sorted lexicographically.
func uniqueNames(templates map[Ref]*template.Template) []string {
	seen := make(map[string]struct{}, len(templates))
	names := make([]string, 0, len(templates))
	for ref := range templates {
		if _, ok := seen[ref.Name]; ok {
			continue
		}
		seen[ref.Name] = struct{}{}
		names = append(names, ref.Name)
	}
	slices.Sort(names)
	return names
}
