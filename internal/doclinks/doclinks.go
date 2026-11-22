// Package doclinks finds relative links in Markdown files whose targets do not
// exist, so documentation cannot silently rot when files move.
package doclinks

import (
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const markdownExt = ".md"

// skippedDirs are never walked: VCS metadata and fixtures whose Markdown is
// test input rather than documentation.
var skippedDirs = map[string]bool{".git": true, "testdata": true, "node_modules": true}

// Broken is a relative link whose target does not exist.
type Broken struct {
	File   string // Markdown file containing the link, slash-separated.
	Target string // Link target, with escapes and character references decoded.
}

// Targets returns the distinct link and image targets in a Markdown document in
// order of appearance, followed in sorted order by targets that only appear in
// reference definitions. Parsing follows CommonMark, so code spans and code
// blocks never yield links.
func Targets(markdown string) []string {
	src := []byte(markdown)
	pc := parser.NewContext()
	doc := goldmark.DefaultParser().Parse(text.NewReader(src), parser.WithContext(pc))

	var targets []string
	seen := make(map[string]bool)
	add := func(dest []byte) {
		if t := decode(dest); !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	addLinks(doc, add)
	// Definitions no link uses still have to point somewhere real.
	refs := pc.References()
	defs := make([]string, 0, len(refs))
	for _, ref := range refs {
		defs = append(defs, string(ref.Destination()))
	}
	slices.Sort(defs)
	for _, d := range defs {
		add([]byte(d))
	}
	return targets
}

// decode resolves backslash escapes and character references in a link
// destination, as a CommonMark renderer would, so "a&#46;md" checks "a.md".
func decode(dest []byte) string {
	dest = util.UnescapePunctuations(dest)
	dest = util.ResolveNumericReferences(dest)
	return string(util.ResolveEntityNames(dest))
}

// addLinks calls add with the destination of every link and image under n.
func addLinks(n ast.Node, add func(dest []byte)) {
	switch n := n.(type) {
	case *ast.Link:
		add(n.Destination)
	case *ast.Image:
		add(n.Destination)
		// An image's children are its alt text, which renders as plain text,
		// so links nested there are not links.
		return
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		addLinks(c, add)
	}
}

// Check walks fsys and returns every relative link in a Markdown file whose
// target is missing. External URLs and same-page anchors are not checked.
func Check(fsys fs.FS) ([]Broken, error) {
	var broken []Broken
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name != "." && skippedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if path.Ext(name) != markdownExt {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		for _, target := range Targets(string(data)) {
			if !exists(fsys, name, target) {
				broken = append(broken, Broken{File: name, Target: target})
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking docs: %w", err)
	}
	return broken, nil
}

// exists reports whether target, linked from file, resolves to a path in fsys.
// Targets with a scheme or host are external and count as existing; a leading
// slash is relative to the root of fsys.
func exists(fsys fs.FS, file, target string) bool {
	// Markdown allows characters a URL does not, such as a literal "100%.md";
	// escape them as goldmark's renderer does, keeping existing %XX escapes.
	u, err := url.Parse(string(util.URLEscape([]byte(target), false)))
	if err != nil {
		return false
	}
	if u.Scheme != "" || u.Host != "" {
		return true
	}
	base := path.Dir(file)
	if strings.HasPrefix(u.Path, "/") {
		base = "."
	}
	info, err := fs.Stat(fsys, path.Join(base, strings.TrimPrefix(u.Path, "/")))
	if err != nil {
		return false
	}
	// path.Join drops a trailing slash, but GitHub serves "README.md/" as 404.
	return info.IsDir() || !strings.HasSuffix(u.Path, "/")
}
