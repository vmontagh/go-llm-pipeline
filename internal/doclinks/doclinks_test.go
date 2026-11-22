package doclinks_test

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/doclinks"
)

// repoRoot is the module root relative to this package directory.
const repoRoot = "../.."

var (
	errInjectedRead = errors.New("injected read failure")
	errInjectedList = errors.New("injected list failure")
)

func TestTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		markdown string
		want     []string
	}{
		{name: "inline link", markdown: "See [the guide](docs/guide.md).", want: []string{"docs/guide.md"}},
		{name: "link in image alt text ignored", markdown: "![foo [bar](missing.md)](logo.png)", want: []string{"logo.png"}},
		{name: "image inside link", markdown: "[![badge](img.svg)](docs/a.md)", want: []string{"docs/a.md", "img.svg"}},
		{name: "image and title", markdown: `![logo](img/logo.png "Logo")`, want: []string{"img/logo.png"}},
		{name: "single-quoted title", markdown: "[g](missing.md 'Guide')", want: []string{"missing.md"}},
		{name: "parenthesized title", markdown: "[g](missing.md (Guide))", want: []string{"missing.md"}},
		{name: "balanced parentheses", markdown: "[g](docs/guide(v2).md)", want: []string{"docs/guide(v2).md"}},
		{name: "escaped parenthesis", markdown: `[g](docs/guide\(.md)`, want: []string{"docs/guide(.md"}},
		{name: "numeric character reference", markdown: "[x](docs/a&#46;md)", want: []string{"docs/a.md"}},
		{name: "named character reference", markdown: "[x](a&amp;b.md)", want: []string{"a&b.md"}},
		{name: "reference definition character reference", markdown: "[x]: caf&eacute;&#x2E;md\n", want: []string{"café.md"}},
		{name: "escaped ampersand stays literal", markdown: `[x](a\&amp;b.md)`, want: []string{"a&amp;b.md"}},
		{name: "decoded reference not decoded again", markdown: "[x](&#38;amp;.md)", want: []string{"&amp;.md"}},
		{name: "decoded backslash not an escape", markdown: "[x](&#92;&#40;.md)", want: []string{`\(.md`}},
		{name: "backslash before letter kept", markdown: `[x](a\b.md)`, want: []string{`a\b.md`}},
		{name: "reference needs semicolon", markdown: "[x](a&ampb.md)", want: []string{"a&ampb.md"}},
		{name: "unknown reference with known prefix kept", markdown: "[x](a&notit;.md)", want: []string{"a&notit;.md"}},
		{name: "unknown reference kept", markdown: "[x](a&bogus;.md)", want: []string{"a&bogus;.md"}},
		{name: "reference name is case sensitive", markdown: "[x](a&Amp;b.md)", want: []string{"a&Amp;b.md"}},
		{name: "numeric reference has no legacy remap", markdown: "[x](a&#128;.md)", want: []string{"a\u0080.md"}},
		{name: "hex reference", markdown: "[x](a&#X2e;md)", want: []string{"a.md"}},
		{name: "null reference becomes replacement", markdown: "[x](a&#0;.md)", want: []string{"a\uFFFD.md"}},
		{name: "surrogate reference becomes replacement", markdown: "[x](a&#xD800;.md)", want: []string{"a\uFFFD.md"}},
		{name: "out of range reference becomes replacement", markdown: "[x](a&#x110000;.md)", want: []string{"a\uFFFD.md"}},
		{name: "escape after reference", markdown: `[x](&amp;\(.md)`, want: []string{"&(.md"}},
		{name: "angle brackets", markdown: "[x](<a b.md>)", want: []string{"a b.md"}},
		{name: "several per line", markdown: "[a](a.md) and [b](b.md#top)", want: []string{"a.md", "b.md#top"}},
		{name: "duplicates reported once", markdown: "[a](a.md) [again](a.md)", want: []string{"a.md"}},
		{name: "reference definition", markdown: "[guide]: ./docs/guide.md\n", want: []string{"./docs/guide.md"}},
		{name: "angle reference definition", markdown: "[g]: <my guide.md> \"Title\"", want: []string{"my guide.md"}},
		{name: "used reference reported once", markdown: "[x][g]\n\n[g]: g.md\n", want: []string{"g.md"}},
		{name: "unused definitions sorted", markdown: "[z]: z.md\n[a]: a.md\n", want: []string{"a.md", "z.md"}},
		{name: "fenced block ignored", markdown: "```\n[a](gone.md)\n```\n[b](kept.md)", want: []string{"kept.md"}},
		{name: "tilde fence ignored", markdown: "~~~go\n[a](gone.md)\n~~~\n", want: nil},
		{name: "shorter fence inside longer", markdown: "````\n```\n[a](gone.md)\n```\n````\n[b](kept.md)", want: []string{"kept.md"}},
		{name: "other fence char inside", markdown: "```\n~~~\n[a](gone.md)\n```\n[b](kept.md)", want: []string{"kept.md"}},
		{name: "inline code ignored", markdown: "use `[a](gone.md)` here", want: nil},
		{name: "double backtick code ignored", markdown: "use ``[a](gone.md) ` x`` here", want: nil},
		{name: "no links", markdown: "plain text", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := doclinks.Targets(tt.markdown)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Targets(%q) mismatch (-want +got):\n%s", tt.markdown, diff)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"README.md":           {Data: []byte("[ok](docs/a.md) [abs](/LICENSE) [web](https://x.test/y.md) [anchor](#top) [mail](mailto:a@x.test) [proto](//x.test/a.md) [file](file:///missing.md)\n[gone](docs/missing.md)")},
		"LICENSE":             {},
		"docs/a.md":           {Data: []byte("[up](../README.md#usage) [esc](../../outside.md) [enc](my%20file.md) [root](/docs/../LICENSE) [pct](100%.md) [ref](caf&eacute;&#46;md) [pctgone](50%.md) [dir](../docs/) [slashfile](../LICENSE/)")},
		"docs/100%.md":        {},
		"docs/café.md":        {},
		"docs/my file.md":     {},
		"docs/urls.md":        {Data: []byte(`[v6](http://[::1]/x) [v6port](https://[2001:db8::1]:8080/a) [v6net](//[::1]/x) [colon](1:x.md) [bs](..\docs\a.md) [bsnet](\\\\x.test/a.md) [bsgone](..\missing.md) [badpct](%zz.md) [query](a.md?plain=1#top) [qmark](a%3F.md) [pointy](< a.md >) [ctl](a&#9;&#10;.&#13;md)`)},
		"docs/1:x.md":         {},
		"docs/%zz.md":         {},
		"docs/a?.md":          {},
		"docs/notes.txt":      {Data: []byte("[ignored](missing.md)")},
		"testdata/fixture.md": {Data: []byte("[fixture](missing.md)")},
	}
	want := []doclinks.Broken{
		{File: "README.md", Target: "docs/missing.md"},
		{File: "docs/a.md", Target: "../../outside.md"},
		{File: "docs/a.md", Target: "50%.md"},
		{File: "docs/a.md", Target: "../LICENSE/"},
		{File: "docs/urls.md", Target: `..\missing.md`},
	}

	got, err := doclinks.Check(fsys)

	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Check() mismatch (-want +got):\n%s", diff)
	}
}

func TestCheckFilesystemErrors(t *testing.T) {
	t.Parallel()
	docs := fstest.MapFS{"docs/a.md": {Data: []byte("[x](x.md)")}}
	tests := []struct {
		name string
		fsys fs.FS
		want error
	}{
		{name: "walk failure", fsys: failingFS{MapFS: docs, failList: "docs", err: errInjectedList}, want: errInjectedList},
		{name: "read failure", fsys: failingFS{MapFS: docs, failRead: "docs/a.md", err: errInjectedRead}, want: errInjectedRead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := doclinks.Check(tt.fsys)

			if diff := cmp.Diff(tt.want, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Check() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRepositoryDocsHaveNoDeadLinks(t *testing.T) {
	t.Parallel()

	got, err := doclinks.Check(os.DirFS(repoRoot))

	if err != nil {
		t.Fatalf("Check(%q) error = %v", repoRoot, err)
	}
	for _, b := range got {
		t.Errorf("%s links to missing %q", b.File, b.Target)
	}
}

// failingFS is a MapFS that returns err when reading the file failRead or
// listing the directory failList.
type failingFS struct {
	fstest.MapFS
	failRead string
	failList string
	err      error
}

func (f failingFS) ReadFile(name string) ([]byte, error) {
	if name == f.failRead {
		return nil, f.err
	}
	return f.MapFS.ReadFile(name)
}

func (f failingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == f.failList {
		return nil, f.err
	}
	return f.MapFS.ReadDir(name)
}
