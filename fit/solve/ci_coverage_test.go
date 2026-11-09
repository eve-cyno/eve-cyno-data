package solve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every test that skips or shrinks itself under -race (skipHeavyRace, shrinkUnderRace) must be
// named in the non-race CI step for ./fit/solve/, so no coverage is lost by the split.
func TestCI_NonRaceStepRunsEveryHeavyTest(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files: %v", err)
	}
	var heavy []string
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fd.Name.Name, "Test") || fd.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && (id.Name == "skipHeavyRace" || id.Name == "shrinkUnderRace" || id.Name == "skipHeavyCorpus") {
						found = true
					}
				}
				return !found
			})
			if found {
				heavy = append(heavy, fd.Name.Name)
			}
		}
	}
	sort.Strings(heavy)
	if len(heavy) == 0 {
		t.Fatal("no heavy tests found: the marker helpers are unused")
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if os.IsNotExist(err) {
		t.Skip("ci.yml not present (source tree without .github)")
	}
	if err != nil {
		t.Fatal(err)
	}
	var step string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "./fit/solve/") && strings.Contains(line, "go test") && strings.Contains(line, "-run") {
			step = line
		}
	}
	if step == "" {
		t.Fatal("ci.yml has no `go test -run ... ./fit/solve/` step")
	}
	if strings.Contains(step, "-race") {
		t.Fatalf("the solve heavy step must run WITHOUT -race: %s", step)
	}
	named := map[string]bool{}
	for _, n := range regexp.MustCompile(`Test\w+`).FindAllString(step, -1) {
		named[n] = true
	}
	for _, h := range heavy {
		if !named[h] {
			t.Errorf("%s skips/shrinks under -race but is missing from the ci.yml non-race step", h)
		}
	}
	for n := range named {
		found := false
		for _, h := range heavy {
			found = found || h == n
		}
		if !found {
			t.Errorf("ci.yml names %s but it is not marked heavy", n)
		}
	}
}
