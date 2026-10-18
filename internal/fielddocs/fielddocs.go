// Package fielddocs extracts the doc comments of struct fields from Go source, so the
// generated API description can say what each field of a typed result means. Reflection
// cannot reach comments; this package reads them from the source and the result is
// committed as dataapi/fielddocs.json (see dataapi/generate.go). TestFieldDocsAreCurrent
// in dataapi fails when the committed file is stale.
package fielddocs

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Dirs are the packages (relative to the repository root) whose structs the API encodes.
func Dirs() []string { return []string{"tools", "fit", "rag", "sde"} }

// Key is the lookup key of a field: the package directory name, the Go type name and the
// Go field name ("tools.MarketPrice.BestSellISK").
func Key(pkg, typ, field string) string { return pkg + "." + typ + "." + field }

// Generate reads the non-test Go files of Dirs under root and returns the field docs as
// indented JSON: an object from Key to the comment text (whitespace collapsed), keys sorted.
// Fields without a comment are left out.
func Generate(root string) ([]byte, error) {
	docs := map[string]string{}
	for _, dir := range Dirs() {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", dir, err)
		}
		fset := token.NewFileSet()
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(root, dir, name), nil, parser.ParseComments)
			if err != nil {
				return nil, fmt.Errorf("parse %s/%s: %w", dir, name, err)
			}
			collect(file, filepath.Base(dir), docs)
		}
	}
	b, err := json.MarshalIndent(docs, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode field docs: %w", err)
	}
	return append(b, '\n'), nil
}

func collect(file *ast.File, pkg string, docs map[string]string) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, f := range st.Fields.List {
				text := strings.Join(strings.Fields(commentText(f)), " ")
				if text == "" {
					continue
				}
				for _, name := range f.Names {
					if name.IsExported() {
						docs[Key(pkg, ts.Name.Name, name.Name)] = text
					}
				}
			}
		}
	}
}

// commentText is the doc comment above a field, else the trailing comment on its line.
func commentText(f *ast.Field) string {
	if f.Doc != nil {
		return f.Doc.Text()
	}
	if f.Comment != nil {
		return f.Comment.Text()
	}
	return ""
}
