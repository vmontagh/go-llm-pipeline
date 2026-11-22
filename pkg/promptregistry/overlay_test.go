package promptregistry_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"text/template"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/vmontagh/go-llm-pipeline/pkg/promptregistry"
)

// photoRulesID is the ID of testdata/overlays/photo-rules.yaml. It is stored
// with every output, so a change to the hash encoding must fail this test.
const photoRulesID = "photo-rules@7#d3c48e706130"

// newOverlay builds an overlay from spec, failing the test on error.
func newOverlay(t *testing.T, spec promptregistry.OverlaySpec) *promptregistry.Overlay {
	t.Helper()
	o, err := promptregistry.NewOverlay(spec)
	if err != nil {
		t.Fatalf("NewOverlay(%+v) error: %v", spec, err)
	}
	return o
}

// loadPhotoRules loads the fixture overlay used by the golden and example
// tests.
func loadPhotoRules(t *testing.T) *promptregistry.Overlay {
	t.Helper()
	o, err := promptregistry.LoadOverlay(os.DirFS("testdata/overlays"), "photo-rules.yaml")
	if err != nil {
		t.Fatalf("LoadOverlay() error: %v", err)
	}
	return o
}

// registry builds a registry holding src as prompt/v1.
func registry(t *testing.T, src string, opts ...promptregistry.Option) *promptregistry.Registry {
	t.Helper()
	reg, err := promptregistry.New(mapFS(map[string]string{"prompt/v1.tmpl": src}), opts...)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return reg
}

func TestNewOverlay(t *testing.T) {
	t.Parallel()
	valid := func() promptregistry.OverlaySpec {
		return promptregistry.OverlaySpec{
			Name:     "photo-rules",
			Version:  "v1.2_rc-1",
			Sections: map[string][]string{"guidelines": {"a", "b"}, "_empty": nil},
		}
	}
	tests := []struct {
		name    string
		edit    func(*promptregistry.OverlaySpec)
		wantErr error
	}{
		{name: "valid", edit: func(*promptregistry.OverlaySpec) {}},
		{name: "no sections", edit: func(s *promptregistry.OverlaySpec) { s.Sections = nil }},
		{name: "empty name", edit: func(s *promptregistry.OverlaySpec) { s.Name = "" }, wantErr: promptregistry.ErrInvalidOverlay},
		{name: "empty version", edit: func(s *promptregistry.OverlaySpec) { s.Version = "" }, wantErr: promptregistry.ErrInvalidOverlay},
		{name: "ID separator in name", edit: func(s *promptregistry.OverlaySpec) { s.Name = "a+b" }, wantErr: promptregistry.ErrInvalidOverlay},
		{name: "ID separator in version", edit: func(s *promptregistry.OverlaySpec) { s.Version = "7#x" }, wantErr: promptregistry.ErrInvalidOverlay},
		{name: "section not an identifier", edit: func(s *promptregistry.OverlaySpec) { s.Sections["no-dash"] = nil }, wantErr: promptregistry.ErrInvalidOverlay},
		{name: "section starts with digit", edit: func(s *promptregistry.OverlaySpec) { s.Sections["1st"] = nil }, wantErr: promptregistry.ErrInvalidOverlay},
		{name: "blank rule", edit: func(s *promptregistry.OverlaySpec) { s.Sections["guidelines"] = []string{"a", " \t"} }, wantErr: promptregistry.ErrInvalidOverlay},
		{name: "duplicate rule", edit: func(s *promptregistry.OverlaySpec) { s.Sections["guidelines"] = []string{"a", "a"} }, wantErr: promptregistry.ErrInvalidOverlay},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec := valid()
			tt.edit(&spec)

			_, err := promptregistry.NewOverlay(spec)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("NewOverlay(%+v) error mismatch (-want +got):\n%s", spec, diff)
			}
		})
	}
}

func TestNewOverlayJoinsErrors(t *testing.T) {
	t.Parallel()
	spec := promptregistry.OverlaySpec{
		Name:     "a+b",
		Version:  "",
		Sections: map[string][]string{"no-dash": {" ", "x", "x"}},
	}
	const wantErrs = 5 // name, version, section, blank rule, duplicate rule

	_, err := promptregistry.NewOverlay(spec)

	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("NewOverlay(%+v) error = %v, want a joined error", spec, err)
	}
	errs := joined.Unwrap()
	if diff := cmp.Diff(wantErrs, len(errs)); diff != "" {
		t.Errorf("NewOverlay(%+v) joined error count mismatch (-want +got):\n%s", spec, diff)
	}
	for _, e := range errs {
		if !errors.Is(e, promptregistry.ErrInvalidOverlay) {
			t.Errorf("NewOverlay(%+v) joined error %v does not wrap ErrInvalidOverlay", spec, e)
		}
	}
}

func TestOverlayCopiesSections(t *testing.T) {
	t.Parallel()
	spec := promptregistry.OverlaySpec{Name: "r", Version: "1", Sections: map[string][]string{"s": {"a"}}}
	o := newOverlay(t, spec)
	want := map[string][]string{"s": {"a"}}

	spec.Sections["s"][0] = "changed by spec"
	o.Sections()["s"][0] = "changed by accessor"

	if diff := cmp.Diff(want, o.Sections()); diff != "" {
		t.Errorf("Sections() mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadOverlay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     string
		want    map[string][]string
		wantErr error
	}{
		{
			name: "valid",
			src:  "name: r\nversion: 7\nsections:\n  guidelines: [a, b]\n  rejection:\n",
			want: map[string][]string{"guidelines": {"a", "b"}, "rejection": nil},
		},
		{name: "unknown field", src: "name: r\nversion: 1\nrules: []\n", wantErr: promptregistry.ErrInvalidOverlay},
		{name: "two documents", src: "name: r\nversion: 1\n---\nname: s\n", wantErr: promptregistry.ErrInvalidOverlay},
		{name: "not YAML", src: "name: [", wantErr: promptregistry.ErrInvalidOverlay},
		{name: "invalid overlay", src: "name: r\nversion: 1\nsections:\n  s: ['']\n", wantErr: promptregistry.ErrInvalidOverlay},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fsys := mapFS(map[string]string{"o.yaml": tt.src})

			o, err := promptregistry.LoadOverlay(fsys, "o.yaml")

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("LoadOverlay(%q) error mismatch (-want +got):\n%s", tt.src, diff)
			}
			if err != nil {
				return
			}
			if diff := cmp.Diff(tt.want, o.Sections()); diff != "" {
				t.Errorf("LoadOverlay(%q) sections mismatch (-want +got):\n%s", tt.src, diff)
			}
		})
	}
}

func TestLoadOverlayMissingFile(t *testing.T) {
	t.Parallel()

	_, err := promptregistry.LoadOverlay(mapFS(nil), "missing.yaml")

	if !errors.Is(err, promptregistry.ErrInvalidOverlay) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadOverlay(missing) error = %v, want ErrInvalidOverlay wrapping fs.ErrNotExist", err)
	}
}

func TestLoadOverlayNilFS(t *testing.T) {
	t.Parallel()

	_, err := promptregistry.LoadOverlay(nil, "rules.yaml")

	if diff := cmp.Diff(promptregistry.ErrInvalidOverlay, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("LoadOverlay(nil) error mismatch (-want +got):\n%s", diff)
	}
}

func TestVersionID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ref     promptregistry.Ref
		overlay *promptregistry.Overlay
		want    string
	}{
		{name: "no overlay", ref: promptregistry.Ref{Name: "judge", Version: "v1"}, want: "judge/v1"},
		{name: "overlay", ref: promptregistry.Ref{Name: "judge", Version: "v1"}, overlay: loadPhotoRules(t), want: "judge/v1+" + photoRulesID},
		{name: "plus in template escaped", ref: promptregistry.Ref{Name: "judge", Version: "v1+" + photoRulesID}, want: "judge/v1%2B" + photoRulesID},
		{name: "percent in template escaped", ref: promptregistry.Ref{Name: "a%2Bb", Version: "v1"}, want: "a%252Bb/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := promptregistry.VersionID(tt.ref, tt.overlay); got != tt.want {
				t.Errorf("VersionID(%v) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestOverlayHash(t *testing.T) {
	t.Parallel()
	base := map[string][]string{"a": {"x", "y"}, "b": {"z"}}
	tests := []struct {
		name     string
		sections map[string][]string
		wantSame bool
	}{
		{name: "same content", sections: map[string][]string{"b": {"z"}, "a": {"x", "y"}}, wantSame: true},
		{name: "rule edited", sections: map[string][]string{"a": {"x", "Y"}, "b": {"z"}}},
		{name: "rules reordered", sections: map[string][]string{"a": {"y", "x"}, "b": {"z"}}},
		{name: "rule moved to other section", sections: map[string][]string{"a": {"x"}, "b": {"y", "z"}}},
		{name: "rules joined", sections: map[string][]string{"a": {"xy"}, "b": {"z"}}},
		{name: "empty section added", sections: map[string][]string{"a": {"x", "y"}, "b": {"z"}, "c": nil}},
	}
	want := newOverlay(t, promptregistry.OverlaySpec{Name: "r", Version: "1", Sections: base}).Hash()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := newOverlay(t, promptregistry.OverlaySpec{Name: "other", Version: "2", Sections: tt.sections})

			if got := o.Hash() == want; got != tt.wantSame {
				t.Errorf("Hash(%v) equal to Hash(%v) = %t, want %t", tt.sections, base, got, tt.wantSame)
			}
		})
	}
}

func TestRenderWithOverlay(t *testing.T) {
	t.Parallel()
	rules := map[string][]string{"guidelines": {"be brief", "be kind"}, "rejection": nil}
	callerRules := map[string]any{"Title": "beach", "Rules": map[string][]string{"guidelines": {"caller"}}}
	tests := []struct {
		name     string
		src      string
		data     any
		sections map[string][]string
		opts     []promptregistry.RenderOption
		want     string
		wantErr  error
	}{
		{
			name:     "sections and data",
			src:      `{{.Data.Title}}:{{range .Rules.guidelines}} {{.}};{{end}}{{range .Rules.rejection}}!{{end}}`,
			sections: rules,
			want:     "beach: be brief; be kind;",
		},
		{
			name:     "root variable inside range",
			src:      `{{range .Rules.guidelines}}{{.}}/{{$.Data.Title}} {{end}}`,
			sections: map[string][]string{"guidelines": {"a"}},
			want:     "a/beach ",
		},
		{
			name:     "root variable reference",
			src:      `{{with .Data}}{{range $.Rules.guidelines}}{{.}}{{end}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}},
			want:     "a",
		},
		{
			name:     "parenthesized rules",
			src:      `{{range (.Rules).guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}},
			want:     "a",
		},
		{
			name:     "parenthesized rules skipping a section",
			src:      `{{range (.Rules).guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}, "rejection": {"b"}},
			wantErr:  promptregistry.ErrUnusedRules,
		},
		{
			name:     "parenthesized root",
			src:      `{{range ($).Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}},
			want:     "a",
		},
		{
			name:     "root saved in a variable",
			src:      `{{$root := .}}{{with .Data}}{{range $root.Rules.guidelines}}{{.}}{{end}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}},
			want:     "a",
		},
		{
			name:     "dot changed to caller rules",
			src:      `{{with .Data}}{{range .Rules.guidelines}}{{.}}{{end}}{{end}}`,
			data:     callerRules,
			sections: map[string][]string{"guidelines": {"be brief"}},
			wantErr:  promptregistry.ErrUnusedRules,
		},
		{
			name:     "variable bound to caller rules",
			src:      `{{$d := .Data}}{{range $d.Rules.guidelines}}{{.}}{{end}}`,
			data:     callerRules,
			sections: map[string][]string{"guidelines": {"be brief"}},
			wantErr:  promptregistry.ErrUnusedRules,
		},
		{
			name:     "rules only in a branch that does not run",
			src:      `{{if false}}{{range .Rules.guidelines}}{{.}}{{end}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}},
			wantErr:  promptregistry.ErrUnusedRules,
		},
		{
			name:     "rule only inside a longer rule matches by text",
			src:      `{{index .Rules.guidelines 1}}`,
			sections: map[string][]string{"guidelines": {"no blur", "no blur at night"}},
			want:     "no blur at night",
		},

		{
			name:     "overlapping rules",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"ab", "aba"}},
			want:     "ababa",
		},

		{
			name:     "absent rule after heavily overlapping rules",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {strings.Repeat("a", 100_000), "a"}, "rejection": {"z"}},
			wantErr:  promptregistry.ErrUnusedRules,
		},

		{
			name:     "rule shared by two sections rendered once",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}, "rejection": {"a"}},
			wantErr:  promptregistry.ErrUnusedRules,
		},
		{
			name:     "rule shared by two sections rendered twice",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}{{range .Rules.rejection}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}, "rejection": {"a"}},
			want:     "aa",
		},
		{
			name:     "transformed rule",
			src:      `{{range .Rules.guidelines}}{{printf "%q" .}}{{end}}`,
			sections: map[string][]string{"guidelines": {`say "hi"`}},
			wantErr:  promptregistry.ErrUnusedRules,
		},
		{
			name:     "reference inside define",
			src:      `{{define "g"}}{{range .Rules.guidelines}}{{.}}{{end}}{{end}}{{template "g" .}}`,
			sections: map[string][]string{"guidelines": {"a"}},
			want:     "a",
		},
		{
			name:     "ranging over all rules",
			src:      `{{range $s, $r := .Rules}}{{$s}}:{{range $r}} {{.}}{{end}};{{end}}`,
			sections: rules,
			want:     "guidelines: be brief be kind;rejection:;",
		},
		{
			name:     "unreferenced empty section allowed",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}, "rejection": nil},
			want:     "a",
		},
		{
			name:     "unreferenced section with rules",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"a"}, "rejection": {"b"}},
			wantErr:  promptregistry.ErrUnusedRules,
		},
		{
			name:     "caller field named Rules",
			src:      `{{.Data.Rules.guidelines}}`,
			data:     callerRules,
			sections: map[string][]string{"guidelines": {"b"}},
			wantErr:  promptregistry.ErrUnusedRules,
		},
		{
			name:     "section typo",
			src:      `{{range .Rules.rejecton}}{{.}}{{end}}`,
			sections: map[string][]string{"rejection": nil},
			wantErr:  promptregistry.ErrMissingField,
		},
		{
			name:     "plain template with overlay",
			src:      `{{.Title}}`,
			sections: map[string][]string{},
			wantErr:  promptregistry.ErrMissingField,
		},
		{
			name:     "within limit",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"1234", "56"}},
			opts:     []promptregistry.RenderOption{promptregistry.WithOverlayLimit(6)},
			want:     "123456",
		},
		{
			name:     "over limit",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"1234", "567"}},
			opts:     []promptregistry.RenderOption{promptregistry.WithOverlayLimit(6)},
			wantErr:  promptregistry.ErrOverlayTooLarge,
		},
		{
			name:     "zero limit is no limit",
			src:      `{{range .Rules.guidelines}}{{.}}{{end}}`,
			sections: map[string][]string{"guidelines": {"1234", "567"}},
			opts:     []promptregistry.RenderOption{promptregistry.WithOverlayLimit(0)},
			want:     "1234567",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := registry(t, tt.src)
			o := newOverlay(t, promptregistry.OverlaySpec{Name: "r", Version: "1", Sections: tt.sections})
			opts := append([]promptregistry.RenderOption{promptregistry.WithOverlay(o)}, tt.opts...)

			data := tt.data
			if data == nil {
				data = map[string]any{"Title": "beach"}
			}

			got, err := reg.Render(t.Context(), "prompt", "v1", data, opts...)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Render(%q) error mismatch (-want +got):\n%s", tt.src, diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Render(%q) mismatch (-want +got):\n%s", tt.src, diff)
			}
		})
	}
}

func TestRenderWithoutOverlay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		opts    []promptregistry.RenderOption
		src     string
		want    string
		wantErr error
	}{
		{name: "nil overlay renders plain", opts: []promptregistry.RenderOption{promptregistry.WithOverlay(nil)}, src: `{{.Title}}`, want: "beach"},
		{name: "limit alone renders plain", opts: []promptregistry.RenderOption{promptregistry.WithOverlayLimit(1)}, src: `{{.Title}}`, want: "beach"},
		{name: "overlay template needs overlay", src: `{{.Data.Title}}{{.Rules.guidelines}}`, wantErr: promptregistry.ErrMissingField},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := registry(t, tt.src)

			got, err := reg.Render(t.Context(), "prompt", "v1", map[string]any{"Title": "beach"}, tt.opts...)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Render(%q) error mismatch (-want +got):\n%s", tt.src, diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Render(%q) mismatch (-want +got):\n%s", tt.src, diff)
			}
		})
	}
}

// TestRenderOverlayIsolated checks that a template func mutating the rules it
// receives cannot change the overlay seen by later renders.
func TestRenderOverlayIsolated(t *testing.T) {
	t.Parallel()
	clobber := func(rules []string) string {
		got := strings.Join(rules, ",")
		rules[0] = "clobbered"
		return got
	}
	reg := registry(t, `{{clobber .Rules.guidelines}}`, promptregistry.WithFuncs(template.FuncMap{"clobber": clobber}))
	o := newOverlay(t, promptregistry.OverlaySpec{Name: "r", Version: "1", Sections: map[string][]string{"guidelines": {"a", "b"}}})

	for range 2 {
		got, err := reg.Render(t.Context(), "prompt", "v1", nil, promptregistry.WithOverlay(o))
		if err != nil {
			t.Fatalf("Render() error: %v", err)
		}
		if diff := cmp.Diff("a,b", got); diff != "" {
			t.Errorf("Render() mismatch (-want +got):\n%s", diff)
		}
	}
}
