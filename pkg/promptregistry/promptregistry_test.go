package promptregistry_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"testing/fstest"

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
