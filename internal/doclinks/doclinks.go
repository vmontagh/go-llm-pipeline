// Package doclinks finds relative links in Markdown files whose targets do not
// exist, so documentation cannot silently rot when files move.
package doclinks

import (
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const markdownExt = ".md"

var (
	fencePattern      = regexp.MustCompile("^\\s{0,3}(```|~~~)")
	inlineCodePattern = regexp.MustCompile("`[^`]*`")
	// Targets are either <angle-bracketed>, which may contain spaces, or bare.
	inlineLinkPattern = regexp.MustCompile(`\]\(\s*(?:<([^>]+)>|([^)\s]+))(?:\s+"[^"]*")?\s*\)`)
	refLinkPattern    = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s*(?:<([^>]+)>|(\S+))`)
)

// skippedDirs are never walked: VCS metadata and fixtures whose Markdown is
// test input rather than documentation.
var skippedDirs = map[string]bool{".git": true, "testdata": true, "node_modules": true}

// Broken is a relative link whose target does not exist.
type Broken struct {
	File   string // Markdown file containing the link, slash-separated.
	Target string // Link target as written.
}

// Targets returns the link targets in a Markdown document, in order of
// appearance. Links inside fenced code blocks and inline code spans are ignored.
func Targets(markdown string) []string {
	var targets []string
	inFence := false
	for line := range strings.Lines(markdown) {
		if fencePattern.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		line = inlineCodePattern.ReplaceAllString(line, "")
		if m := refLinkPattern.FindStringSubmatch(line); m != nil {
			targets = append(targets, m[1]+m[2])
		}
		for _, m := range inlineLinkPattern.FindAllStringSubmatch(line, -1) {
			targets = append(targets, m[1]+m[2])
		}
	}
	return targets
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
	u, err := url.Parse(target)
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
	_, err = fs.Stat(fsys, path.Join(base, strings.TrimPrefix(u.Path, "/")))
	return err == nil
}
