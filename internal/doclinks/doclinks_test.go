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
		"docs/notes.txt":      {Data: []byte("[ignored](missing.md)")},
		"testdata/fixture.md": {Data: []byte("[fixture](missing.md)")},
	}
	want := []doclinks.Broken{
		{File: "README.md", Target: "docs/missing.md"},
		{File: "docs/a.md", Target: "../../outside.md"},
		{File: "docs/a.md", Target: "50%.md"},
		{File: "docs/a.md", Target: "../LICENSE/"},
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
