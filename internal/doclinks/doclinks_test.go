package doclinks_test

import (
	"os"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/internal/doclinks"
)

// repoRoot is the module root relative to this package directory.
const repoRoot = "../.."

func TestTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		markdown string
		want     []string
	}{
		{name: "inline link", markdown: "See [the guide](docs/guide.md).", want: []string{"docs/guide.md"}},
		{name: "image and title", markdown: `![logo](img/logo.png "Logo")`, want: []string{"img/logo.png"}},
		{name: "angle brackets", markdown: "[x](<a b.md>)", want: []string{"a b.md"}},
		{name: "several per line", markdown: "[a](a.md) and [b](b.md#top)", want: []string{"a.md", "b.md#top"}},
		{name: "reference definition", markdown: "[guide]: ./docs/guide.md\n", want: []string{"./docs/guide.md"}},
		{name: "angle reference definition", markdown: "[g]: <my guide.md> \"Title\"", want: []string{"my guide.md"}},
		{name: "fenced block ignored", markdown: "```\n[a](gone.md)\n```\n[b](kept.md)", want: []string{"kept.md"}},
		{name: "tilde fence ignored", markdown: "~~~go\n[a](gone.md)\n~~~\n", want: nil},
		{name: "inline code ignored", markdown: "use `[a](gone.md)` here", want: nil},
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
		"docs/a.md":           {Data: []byte("[up](../README.md#usage) [esc](../../outside.md) [enc](my%20file.md) [root](/docs/../LICENSE)")},
		"docs/my file.md":     {},
		"docs/notes.txt":      {Data: []byte("[ignored](missing.md)")},
		"testdata/fixture.md": {Data: []byte("[fixture](missing.md)")},
	}
	want := []doclinks.Broken{
		{File: "README.md", Target: "docs/missing.md"},
		{File: "docs/a.md", Target: "../../outside.md"},
	}

	got, err := doclinks.Check(fsys)

	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Check() mismatch (-want +got):\n%s", diff)
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
