// Package apisnapshot renders the exported API of a Go package as stable text,
// so a checked-in snapshot shows every change to a frozen API in review.
package apisnapshot

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

const gofmtTabWidth = 8

// Render returns the exported declarations of the package in dir: constants,
// variables, types with their exported fields, functions and methods, without
// comments or function bodies. Test files are ignored.
func Render(dir, importPath string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return "", fmt.Errorf("parsing %s: %w", name, err)
		}
		files = append(files, f)
	}
	pkg, err := doc.NewFromFiles(fset, files, importPath)
	if err != nil {
		return "", fmt.Errorf("reading package %s: %w", importPath, err)
	}

	var decls []ast.Decl
	addValues := func(vs []*doc.Value) {
		for _, v := range vs {
			decls = append(decls, v.Decl)
		}
	}
	addFuncs := func(fs []*doc.Func) {
		for _, f := range fs {
			f.Decl.Body = nil
			decls = append(decls, f.Decl)
		}
	}
	addValues(pkg.Consts)
	addValues(pkg.Vars)
	addFuncs(pkg.Funcs)
	for _, t := range pkg.Types {
		decls = append(decls, t.Decl)
		addValues(t.Consts)
		addValues(t.Vars)
		addFuncs(t.Funcs)
		addFuncs(t.Methods)
	}

	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: gofmtTabWidth}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n", pkg.Name)
	for _, d := range decls {
		var buf bytes.Buffer
		if err := cfg.Fprint(&buf, fset, d); err != nil {
			return "", fmt.Errorf("printing %s: %w", importPath, err)
		}
		b.WriteString("\n")
		// Dropped doc comments leave blank lines behind; remove them.
		for line := range strings.Lines(buf.String()) {
			if strings.TrimSpace(line) != "" {
				b.WriteString(line)
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}
