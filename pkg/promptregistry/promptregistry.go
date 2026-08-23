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
	"math"
	"path"
	"reflect"
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

// errStrictIndexMissing is returned by the strict index function when a map
// lookup finds no entry for the requested key. Render maps it to
// ErrMissingField so index-based access fails closed like direct field access.
var errStrictIndexMissing = errors.New("map has no entry for key")

// errStrictIndex wraps the non-missing failures of the strict index function
// (out-of-range, unindexable value, unusable key), which classify as
// ErrRenderFailed.
var errStrictIndex = errors.New("strict index")

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
	if err := validateFuncs(cfg.funcs); err != nil {
		return nil, err
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
		tmpl, err := template.New(p).Option("missingkey=error").Funcs(builtinFuncs).Funcs(cfg.funcs).Parse(string(src))
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
// no name directory, or one whose file name is empty once the extension is
// stripped (for example "foo/.tmpl"), lacks an explicit version; both are
// rejected with ErrInvalidTemplate.
func refFromPath(p string) (Ref, error) {
	name := path.Dir(p)
	if name == "." {
		return Ref{}, fmt.Errorf("%w: %q must be nested under a name directory", ErrInvalidTemplate, p)
	}
	version := strings.TrimSuffix(path.Base(p), templateExt)
	if version == "" {
		return Ref{}, fmt.Errorf("%w: %q has an empty version", ErrInvalidTemplate, p)
	}
	return Ref{Name: name, Version: version}, nil
}

// builtinFuncs are the functions registered in every template ahead of any
// caller-supplied FuncMap. It overrides the text/template index builtin with a
// strict variant so a missing map key fails closed instead of yielding the
// map's zero value.
var builtinFuncs = template.FuncMap{"index": strictIndex}

// strictIndex reimplements the text/template index builtin for slices, arrays,
// strings, and maps, differing in one respect: a map lookup with no matching
// entry returns errStrictIndexMissing instead of the element type's zero value.
// This keeps {{index .M "typo"}} fail-closed, mirroring missingkey=error for
// direct field and key access.
func strictIndex(item any, keys ...any) (any, error) {
	v := reflect.ValueOf(item)
	for _, key := range keys {
		for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			if v.IsNil() {
				return nil, fmt.Errorf("%w: nil value", errStrictIndex)
			}
			v = v.Elem()
		}
		switch v.Kind() {
		case reflect.Array, reflect.Slice, reflect.String:
			i, err := indexInt(key)
			if err != nil {
				return nil, err
			}
			if i < 0 || i >= v.Len() {
				return nil, fmt.Errorf("%w: out of range: %d", errStrictIndex, i)
			}
			v = v.Index(i)
		case reflect.Map:
			kv := reflect.ValueOf(key)
			keyType := v.Type().Key()
			switch {
			case kv.Type().AssignableTo(keyType):
			case kv.Type().ConvertibleTo(keyType):
				kv = kv.Convert(keyType)
			default:
				return nil, fmt.Errorf("%w: cannot index map[%s] with key of type %T", errStrictIndex, keyType, key)
			}
			elem := v.MapIndex(kv)
			if !elem.IsValid() {
				return nil, fmt.Errorf("%w %v", errStrictIndexMissing, kv.Interface())
			}
			v = elem
		default:
			return nil, fmt.Errorf("%w: can't index item of type %s", errStrictIndex, v.Type())
		}
	}
	return v.Interface(), nil
}

// indexInt converts a template index argument to an int, accepting the signed
// and unsigned integer kinds text/template may pass for a numeric index.
func indexInt(key any) (int, error) {
	switch k := reflect.ValueOf(key); k.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(k.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if u := k.Uint(); u <= uint64(math.MaxInt) {
			return int(u), nil
		}
		return 0, fmt.Errorf("%w: index out of range: %d", errStrictIndex, key)
	default:
		return 0, fmt.Errorf("%w: cannot index sequence with key of type %T", errStrictIndex, key)
	}
}

// validateFuncs verifies a caller-supplied FuncMap up front so a bad function
// map surfaces as ErrInvalidTemplate instead of a panic from text/template's
// Funcs (which panics on an invalid name, a non-function value, or an
// unsupported signature) while parsing individual templates.
func validateFuncs(funcs template.FuncMap) (err error) {
	if len(funcs) == 0 {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: invalid template funcs: %v", ErrInvalidTemplate, r)
		}
	}()
	template.New("").Funcs(funcs)
	return nil
}

// Render executes the template registered for (name, version) against data and
// returns the fully rendered string. It returns ErrTemplateNotFound when no
// such template exists, ErrMissingField when data omits a referenced field or
// map key (including via the strict index function), and ErrRenderFailed for
// any other execution failure.
//
// The context is checked for cancellation before execution begins but is not
// stored; because text/template exposes no execution hook, an already-started
// render is not interrupted mid-flight. The parameter keeps room for a future
// remote loader without changing the signature.
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
		if errors.Is(err, errStrictIndexMissing) || isMissingKeyError(err) {
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
// caused by a missing map key or struct field. A failure returned by a
// registered function is prefixed by text/template with "error calling " and is
// treated as a render failure even if its message happens to contain a
// missing-key marker, so function errors stay classified as ErrRenderFailed.
func isMissingKeyError(err error) bool {
	msg := err.Error()
	if strings.Contains(msg, "error calling ") {
		return false
	}
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
