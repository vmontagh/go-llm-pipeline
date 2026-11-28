// Package apisnapshot renders the exported API of a Go package as stable text,
// so a checked-in snapshot shows every change to a frozen API in review.
package apisnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const gofmtTabWidth = 8

var (
	// ErrParse reports a Go file that does not parse.
	ErrParse = errors.New("apisnapshot: invalid Go source")
	// ErrNoGoFiles reports a directory without non-test Go files.
	ErrNoGoFiles = errors.New("apisnapshot: no Go files")
	// ErrPackageMismatch reports files that declare different packages.
	ErrPackageMismatch = errors.New("apisnapshot: files declare different packages")
)

// Render returns the exported declarations of the package in dir: constants,
// variables, types with their exported fields, functions and methods, without
// comments or function bodies. Test files are ignored. An import block lists
// the import path of every package qualifier the declarations use, so changing
// an import alone also changes the snapshot. Render runs go list in dir to
// resolve package names.
func Render(ctx context.Context, dir, importPath string) (string, error) {
	fset := token.NewFileSet()
	files, err := parseDir(fset, dir)
	if err != nil {
		return "", err
	}
	pkg, err := doc.NewFromFiles(fset, files, importPath)
	if err != nil {
		return "", fmt.Errorf("reading package %s: %w", importPath, err)
	}

	decls := exportedDecls(pkg)
	imports, err := usedImports(ctx, dir, fset, files, decls)
	if err != nil {
		return "", err
	}

	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: gofmtTabWidth}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n", pkg.Name)
	if len(imports) > 0 {
		b.WriteString("\nimport (\n")
		for _, imp := range imports {
			fmt.Fprintf(&b, "\t%s\n", imp)
		}
		b.WriteString(")\n")
	}
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

func parseDir(fset *token.FileSet, dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w: %w", name, ErrParse, err)
		}
		if len(files) > 0 && f.Name.Name != files[0].Name.Name {
			return nil, fmt.Errorf("%s declares %q, want %q: %w", name, f.Name.Name, files[0].Name.Name, ErrPackageMismatch)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("reading %s: %w", dir, ErrNoGoFiles)
	}
	return files, nil
}

func exportedDecls(pkg *doc.Package) []ast.Decl {
	var decls []ast.Decl
	addValues := func(vs []*doc.Value) {
		for _, v := range vs {
			decls = append(decls, v.Decl)
		}
	}
	addFuncs := func(fs []*doc.Func) {
		// go/doc has already removed the function bodies.
		for _, f := range fs {
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
	return decls
}

// usedImports returns the sorted import lines, as `name "path"`, for the
// package qualifiers that decls use.
func usedImports(ctx context.Context, dir string, fset *token.FileSet, files []*ast.File, decls []ast.Decl) ([]string, error) {
	names, err := packageNames(ctx, dir)
	if err != nil {
		return nil, err
	}
	// Each file has its own imports, keyed by the name the file uses.
	fileImports := make(map[string]map[string]string, len(files))
	for _, f := range files {
		m := make(map[string]string, len(f.Imports))
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("reading import %s: %w", spec.Path.Value, err)
			}
			name := names[path]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			m[name] = path
		}
		fileImports[fset.Position(f.Pos()).Filename] = m
	}

	seen := make(map[string]bool)
	for _, d := range decls {
		imports := fileImports[fset.Position(d.Pos()).Filename]
		ast.Inspect(d, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok {
				if path, ok := imports[id.Name]; ok {
					seen[fmt.Sprintf("%s %q", id.Name, path)] = true
				}
			}
			return true
		})
	}
	lines := make([]string, 0, len(seen))
	for l := range seen {
		lines = append(lines, l)
	}
	slices.Sort(lines)
	return lines, nil
}

// packageNames maps the import path of each dependency of the package in dir
// to its package name, which can differ from the last path element.
func packageNames(ctx context.Context, dir string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-f", "{{.ImportPath}} {{.Name}}", ".")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing dependencies of %s: %w: %s", dir, err, stderr.String())
	}
	names := make(map[string]string)
	for line := range strings.Lines(string(out)) {
		path, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			names[path] = name
		}
	}
	return names, nil
}
