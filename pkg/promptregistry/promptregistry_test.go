package promptregistry_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/fstest"
	"text/template"

	"github.com/google/go-cmp/cmp"
	"github.com/vmontagh/go-llm-pipeline/pkg/promptregistry"
)

// mapFS builds an in-memory filesystem from path/contents pairs so tests stay
// self-contained and independent of on-disk fixtures.
func mapFS(files map[string]string) fstest.MapFS {
	fsys := make(fstest.MapFS, len(files))
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		files   map[string]string
		opts    []promptregistry.Option
		want    []promptregistry.Ref
		wantErr error
	}{
		{
			name: "nested templates registered",
			files: map[string]string{
				"classify/v1.tmpl":  "a",
				"classify/v2.tmpl":  "b",
				"summarize/v1.tmpl": "c",
				"README.md":         "ignored",
			},
			want: []promptregistry.Ref{
				{Name: "classify", Version: "v1"},
				{Name: "classify", Version: "v2"},
				{Name: "summarize", Version: "v1"},
			},
		},
		{
			name: "root option strips prefix",
			files: map[string]string{
				"prompts/classify/v1.tmpl": "a",
			},
			opts: []promptregistry.Option{promptregistry.WithRoot("prompts")},
			want: []promptregistry.Ref{{Name: "classify", Version: "v1"}},
		},
		{
			name:    "template at root rejected",
			files:   map[string]string{"orphan.tmpl": "a"},
			wantErr: promptregistry.ErrInvalidTemplate,
		},
		{
			name:    "parse error rejected",
			files:   map[string]string{"broken/v1.tmpl": "{{.Name"},
			wantErr: promptregistry.ErrInvalidTemplate,
		},
		{
			name:    "empty version rejected",
			files:   map[string]string{"foo/.tmpl": "a"},
			wantErr: promptregistry.ErrInvalidTemplate,
		},
		{
			name:    "no templates rejected",
			files:   map[string]string{"README.md": "nothing here"},
			wantErr: promptregistry.ErrInvalidTemplate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg, err := promptregistry.New(mapFS(tt.files), tt.opts...)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("New() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("New() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, reg.List()); diff != "" {
				t.Errorf("List() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNewNilFilesystem(t *testing.T) {
	t.Parallel()
	if _, err := promptregistry.New(nil); !errors.Is(err, promptregistry.ErrInvalidTemplate) {
		t.Fatalf("New(nil) error = %v, want %v", err, promptregistry.ErrInvalidTemplate)
	}
}

func TestRender(t *testing.T) {
	t.Parallel()

	fsys := mapFS(map[string]string{
		"greet/v1.tmpl":  "Hello {{.Name}}!",
		"pick/v1.tmpl":   "{{index .Items 5}}",
		"struct/v1.tmpl": "{{.Missing}}",
	})
	reg, err := promptregistry.New(fsys)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}

	tests := []struct {
		name    string
		tmpl    string
		version string
		data    any
		want    string
		wantErr error
	}{
		{
			name:    "renders with data",
			tmpl:    "greet",
			version: "v1",
			data:    map[string]any{"Name": "Ada"},
			want:    "Hello Ada!",
		},
		{
			name:    "unknown template",
			tmpl:    "greet",
			version: "v9",
			data:    map[string]any{"Name": "Ada"},
			wantErr: promptregistry.ErrTemplateNotFound,
		},
		{
			name:    "missing map key fails closed",
			tmpl:    "greet",
			version: "v1",
			data:    map[string]any{},
			wantErr: promptregistry.ErrMissingField,
		},
		{
			name:    "missing struct field fails closed",
			tmpl:    "struct",
			version: "v1",
			data:    struct{ Name string }{Name: "Ada"},
			wantErr: promptregistry.ErrMissingField,
		},
		{
			name:    "execution failure",
			tmpl:    "pick",
			version: "v1",
			data:    map[string]any{"Items": []string{"only"}},
			wantErr: promptregistry.ErrRenderFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := reg.Render(context.Background(), tt.tmpl, tt.version, tt.data)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Render() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Render() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRenderCanceledContext(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{"greet/v1.tmpl": "hi {{.Name}}"}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reg.Render(ctx, "greet", "v1", map[string]any{"Name": "Ada"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Render() error = %v, want %v", err, context.Canceled)
	}
}

// TestRenderStrictIndex verifies the strict index override: a missing map key
// fails closed as ErrMissingField, while valid slice and map indexing render.
func TestRenderStrictIndex(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{
		"maptypo/v1.tmpl":  `{{index .Labels "typo"}}`,
		"mapok/v1.tmpl":    `{{index .Labels "present"}}`,
		"sliceok/v1.tmpl":  `{{index .Items 0}}`,
		"sliceoob/v1.tmpl": `{{index .Items 5}}`,
	}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}

	tests := []struct {
		name    string
		tmpl    string
		data    any
		want    string
		wantErr error
	}{
		{
			name:    "missing map key fails closed",
			tmpl:    "maptypo",
			data:    map[string]any{"Labels": map[string]string{"present": "yes"}},
			wantErr: promptregistry.ErrMissingField,
		},
		{
			name: "present map key renders",
			tmpl: "mapok",
			data: map[string]any{"Labels": map[string]string{"present": "yes"}},
			want: "yes",
		},
		{
			name: "slice index renders",
			tmpl: "sliceok",
			data: map[string]any{"Items": []string{"first", "second"}},
			want: "first",
		},
		{
			name:    "slice index out of range fails",
			tmpl:    "sliceoob",
			data:    map[string]any{"Items": []string{"only"}},
			wantErr: promptregistry.ErrRenderFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := reg.Render(context.Background(), tt.tmpl, "v1", tt.data)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Render() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Render() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRenderStrictIndexIntegerKey verifies that indexing a map with a key that
// text/template's index would adapt lossily fails closed (ErrRenderFailed)
// rather than silently selecting a valid-but-wrong entry. The strict index
// admits only assignable keys and exactly-representable integer-to-integer
// conversions, so out-of-range integers, sign flips, int-to-string rune
// conversions, and float-to-int truncation are all rejected, while assignable
// keys keep working.
func TestRenderStrictIndexIntegerKey(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{
		"int8over/v1.tmpl":   "{{index . 256}}",
		"uint8neg/v1.tmpl":   "{{index . -1}}",
		"uintneg/v1.tmpl":    "{{index . -1}}",
		"int8ok/v1.tmpl":     "{{index . 5}}",
		"floattrunc/v1.tmpl": "{{index . 1.5}}",
		"floatint/v1.tmpl":   "{{index . 2.0}}",
		"float64key/v1.tmpl": "{{index . 1.5}}",
		"intstring/v1.tmpl":  "{{index . 65}}",
		"stringkey/v1.tmpl":  `{{index . "k"}}`,
	}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}

	tests := []struct {
		name    string
		tmpl    string
		data    any
		want    string
		wantErr error
	}{
		{
			name:    "int8 key overflow fails closed",
			tmpl:    "int8over",
			data:    map[int8]string{0: "zero"},
			wantErr: promptregistry.ErrRenderFailed,
		},
		{
			name:    "uint8 key negative fails closed",
			tmpl:    "uint8neg",
			data:    map[uint8]string{1: "one"},
			wantErr: promptregistry.ErrRenderFailed,
		},
		{
			name:    "uint key same-width sign flip fails closed",
			tmpl:    "uintneg",
			data:    map[uint]string{2: "two"},
			wantErr: promptregistry.ErrRenderFailed,
		},
		{
			name: "in-range int8 key renders",
			tmpl: "int8ok",
			data: map[int8]string{5: "five"},
			want: "five",
		},
		{
			name:    "fractional float key fails closed",
			tmpl:    "floattrunc",
			data:    map[int]string{1: "one"},
			wantErr: promptregistry.ErrRenderFailed,
		},
		{
			name:    "whole float key against int map fails closed",
			tmpl:    "floatint",
			data:    map[int]string{2: "two"},
			wantErr: promptregistry.ErrRenderFailed,
		},
		{
			name: "float key against float map renders",
			tmpl: "float64key",
			data: map[float64]string{1.5: "x"},
			want: "x",
		},
		{
			name:    "int key against string map fails closed",
			tmpl:    "intstring",
			data:    map[string]string{"A": "value"},
			wantErr: promptregistry.ErrRenderFailed,
		},
		{
			name: "string key unaffected",
			tmpl: "stringkey",
			data: map[string]string{"k": "v"},
			want: "v",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := reg.Render(context.Background(), tt.tmpl, "v1", tt.data)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Render() error = %v, want %v", err, tt.wantErr)
				}
				if errors.Is(err, promptregistry.ErrMissingField) {
					t.Errorf("Render() error = %v, unexpectedly matched ErrMissingField", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Render() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestWithFuncs verifies caller-supplied functions render.
func TestWithFuncs(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(
		mapFS(map[string]string{"shout/v1.tmpl": "{{upper .Word}}"}),
		promptregistry.WithFuncs(map[string]any{"upper": func(s string) string { return "LOUD:" + s }}),
	)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	got, err := reg.Render(context.Background(), "shout", "v1", map[string]any{"Word": "hi"})
	if err != nil {
		t.Fatalf("Render() unexpected error: %v", err)
	}
	if diff := cmp.Diff("LOUD:hi", got); diff != "" {
		t.Errorf("Render() mismatch (-want +got):\n%s", diff)
	}
}

func TestWithFuncsInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		funcs map[string]any
	}{
		{name: "non-function value", funcs: map[string]any{"upper": 42}},
		{name: "invalid name", funcs: map[string]any{"not a name": func() string { return "" }}},
		{name: "too many return values", funcs: map[string]any{"bad": func() (string, string, error) { return "", "", nil }}},
		{name: "reserved index name", funcs: map[string]any{"index": func(any, ...any) (any, error) { return nil, nil }}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := promptregistry.New(
				mapFS(map[string]string{"t/v1.tmpl": "x"}),
				promptregistry.WithFuncs(tt.funcs),
			)
			if !errors.Is(err, promptregistry.ErrInvalidTemplate) {
				t.Fatalf("New() error = %v, want %v", err, promptregistry.ErrInvalidTemplate)
			}
		})
	}
}

// errFuncBoom is returned by a test func whose message deliberately contains a
// missing-key marker to prove function errors classify as ErrRenderFailed.
var errFuncBoom = errors.New("map has no entry for key \"x\"")

// TestRenderFuncError guards that a failure returned by a registered function
// is classified as ErrRenderFailed even when its message contains a
// missing-key marker, rather than being misreported as ErrMissingField.
func TestRenderFuncError(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(
		mapFS(map[string]string{"boom/v1.tmpl": "{{explode}}"}),
		promptregistry.WithFuncs(map[string]any{
			"explode": func() (string, error) {
				return "", errFuncBoom
			},
		}),
	)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_, err = reg.Render(context.Background(), "boom", "v1", nil)
	if !errors.Is(err, promptregistry.ErrRenderFailed) {
		t.Fatalf("Render() error = %v, want %v", err, promptregistry.ErrRenderFailed)
	}
	if errors.Is(err, promptregistry.ErrMissingField) {
		t.Errorf("Render() error = %v, unexpectedly matched ErrMissingField", err)
	}
}

// TestRenderMissingFieldTemplateNameCollision guards that a genuine missing
// field is classified as ErrMissingField even when the template name itself
// contains the "error calling " marker, which the name-scrub in
// isMissingKeyError must strip before inspecting the message.
func TestRenderMissingFieldTemplateNameCollision(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{
		"error calling prompt/v1.tmpl": "{{.Missing}}",
	}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_, err = reg.Render(context.Background(), "error calling prompt", "v1", struct{ Name string }{})
	if !errors.Is(err, promptregistry.ErrMissingField) {
		t.Fatalf("Render() error = %v, want %v", err, promptregistry.ErrMissingField)
	}
	if errors.Is(err, promptregistry.ErrRenderFailed) {
		t.Errorf("Render() error = %v, unexpectedly matched ErrRenderFailed", err)
	}
}

// TestRenderMissingFieldNameCollidesWithCause locks the classification of a
// genuine missing map key for a template whose name shares the "map" prefix of
// text/template's cause payload (`map has no entry for key ...`). It guards the
// wrapper-only name scrub in isMissingKeyError: only the two name-bearing
// wrapper tokens are stripped, so the cause is never corrupted and the error
// still classifies as ErrMissingField.
func TestRenderMissingFieldNameCollidesWithCause(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{
		"map/v1.tmpl": "{{.Missing}}",
	}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_, err = reg.Render(context.Background(), "map", "v1", map[string]string{})
	if !errors.Is(err, promptregistry.ErrMissingField) {
		t.Fatalf("Render() error = %v, want %v", err, promptregistry.ErrMissingField)
	}
	if errors.Is(err, promptregistry.ErrRenderFailed) {
		t.Errorf("Render() error = %v, unexpectedly matched ErrRenderFailed", err)
	}
}

// TestRenderMissingFieldQuotedNameWithPhrase guards the quoted-name wrapper
// scrub. text/template formats the "executing %q" token with strconv.Quote, so
// a template whose name contains a quote (and the "error calling " phrase) is
// escaped there. Matching that token with strconv.Quote strips it, leaving no
// function-call wrapper before the marker, so a genuine missing key still
// classifies as ErrMissingField instead of ErrRenderFailed.
func TestRenderMissingFieldQuotedNameWithPhrase(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{
		`error calling "x/v1.tmpl`: "{{.Missing}}",
	}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_, err = reg.Render(context.Background(), `error calling "x`, "v1", map[string]string{})
	if !errors.Is(err, promptregistry.ErrMissingField) {
		t.Fatalf("Render() error = %v, want %v", err, promptregistry.ErrMissingField)
	}
	if errors.Is(err, promptregistry.ErrRenderFailed) {
		t.Errorf("Render() error = %v, unexpectedly matched ErrRenderFailed", err)
	}
}

// text/template formats a data-eval failure as
// `template: <name>:<line>:<col>: executing "<name>" at <ctx>: <cause>`, so the
// name appears only in the two wrapper tokens; these fixtures let the test
// exercise a name that also matches the cause payload. A genuine missing key or
// field is a leaf error, whereas a function-call failure is %w-wrapped
// (errExecFuncCall wraps errExecFuncCallCause), which is what the leaf gate in
// isMissingKeyError keys on.
var (
	errExecNameCollidesCause  = errors.New(`template: map:1:11: executing "map" at <.Missing>: map has no entry for key "Missing"`)
	errExecFuncCallCause      = errors.New(`map has no entry for key "x"`)
	errExecFuncCall           = fmt.Errorf(`template: t:1:5: executing "t" at <fail>: error calling fail: %w`, errExecFuncCallCause)
	errExecPlainField         = errors.New(`template: t:1:2: executing "t" at <.Missing>: can't evaluate field Missing in type struct {}`)
	errExecFieldTypeHasPhrase = errors.New(`template: t:1:2: executing "t" at <.Missing>: can't evaluate field Missing in type struct { F string "error calling x" }`)
	errExecMarkerHasPhrase    = errors.New(`template: t:1:11: executing "t" at <.Missing>: map has no entry for key "error calling "`)
)

// TestIsMissingKeyErrorNameCollision is the white-box guard for the wrapper-only
// name scrub. The public Render API cannot produce a bare template name (names
// are full file paths), so a collision between the name and a cause payload is
// only reachable by constructing a synthetic ExecError here. The first case
// fails on the previous global-ReplaceAll scrub, which strips "map" from the
// cause and drops the marker.
func TestIsMissingKeyErrorNameCollision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "name collides with missing-key cause",
			err:  template.ExecError{Name: "map", Err: errExecNameCollidesCause},
			want: true,
		},
		{
			name: "function-call failure stays render-failed",
			err:  template.ExecError{Name: "t", Err: errExecFuncCall},
			want: false,
		},
		{
			name: "plain missing field",
			err:  template.ExecError{Name: "t", Err: errExecPlainField},
			want: true,
		},
		{
			name: "missing field whose type contains error-calling phrase",
			err:  template.ExecError{Name: "t", Err: errExecFieldTypeHasPhrase},
			want: true,
		},
		{
			name: "missing key whose payload contains error-calling phrase after marker",
			err:  template.ExecError{Name: "t", Err: errExecMarkerHasPhrase},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := promptregistry.IsMissingKeyError(tt.err); got != tt.want {
				t.Errorf("IsMissingKeyError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

// errBoom is a registered function's failure whose message contains a
// missing-key marker; text/template wraps it as "error calling fail: ...".
var errBoom = errors.New("map has no entry for key")

// TestRenderFuncErrorMarkerInMessage guards the leaf-vs-wrapped classification:
// a registered function that fails with a message containing a missing-key
// marker (or whose argument does) must classify as ErrRenderFailed, not
// ErrMissingField, because text/template %w-wraps the function failure so its
// execution error is non-leaf.
func TestRenderFuncErrorMarkerInMessage(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(
		mapFS(map[string]string{
			"boom/v1.tmpl": `{{fail "map has no entry for key"}}`,
		}),
		promptregistry.WithFuncs(template.FuncMap{
			"fail": func(string) (string, error) { return "", errBoom },
		}),
	)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_, err = reg.Render(context.Background(), "boom", "v1", nil)
	if !errors.Is(err, promptregistry.ErrRenderFailed) {
		t.Fatalf("Render() error = %v, want %v", err, promptregistry.ErrRenderFailed)
	}
	if errors.Is(err, promptregistry.ErrMissingField) {
		t.Errorf("Render() error = %v, unexpectedly matched ErrMissingField", err)
	}
}

func TestNames(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{
		"summarize/v1.tmpl": "a",
		"classify/v1.tmpl":  "b",
		"classify/v2.tmpl":  "c",
	}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if diff := cmp.Diff([]string{"classify", "summarize"}, reg.Names()); diff != "" {
		t.Errorf("Names() mismatch (-want +got):\n%s", diff)
	}
}

func TestNamesReturnsCopy(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{"a/v1.tmpl": "x", "b/v1.tmpl": "y"}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	names := reg.Names()
	names[0] = "mutated"
	if got := reg.Names()[0]; got == "mutated" {
		t.Errorf("Names() shares backing array; mutation leaked: got %q", got)
	}
}

func TestRenderConcurrent(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(mapFS(map[string]string{"greet/v1.tmpl": "Hello {{.Name}}!"}))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	const goroutines = 32
	errs := make(chan error, goroutines)
	for i := range goroutines {
		go func() {
			_, err := reg.Render(context.Background(), "greet", "v1", map[string]any{"Name": i})
			errs <- err
		}()
	}
	for range goroutines {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Render() error: %v", err)
		}
	}
}

// TestFixturesRegistered guards that the on-disk fixture templates load through
// the same public API downstream callers use with an embed.FS.
func TestFixturesRegistered(t *testing.T) {
	t.Parallel()
	reg, err := promptregistry.New(os.DirFS("testdata/templates"))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	want := []promptregistry.Ref{
		{Name: "classify_asset", Version: "v1"},
		{Name: "classify_asset", Version: "v2"},
		{Name: "summarize", Version: "v1"},
	}
	if diff := cmp.Diff(want, reg.List()); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}
}
