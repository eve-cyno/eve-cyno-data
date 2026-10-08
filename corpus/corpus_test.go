package corpus

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/schema.golden from the constants of this package")

const goldenPath = "testdata/schema.golden"

// declared parses the non-test source files of this package and returns every
// exported constant with a literal value: name -> value (strings unquoted, ints
// in decimal). The constants ARE the schema, so the tests below read them from
// the source instead of keeping a second list that could drift.
func declared(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !name.IsExported() || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					require.Truef(t, ok, "%s must be a literal constant so the golden snapshot can pin it", name.Name)
					v := lit.Value
					if lit.Kind == token.STRING {
						v, err = strconv.Unquote(lit.Value)
						require.NoError(t, err)
					}
					require.NotContainsf(t, out, name.Name, "duplicate constant %s", name.Name)
					out[name.Name] = v
				}
			}
		}
	}
	return out
}

// TestKeysListsEveryKeyConstant is the "constant added on one side only" guard:
// a Key* constant that is not in Keys() (or a Keys() entry that is no constant)
// fails here, so a writer cannot start emitting a key the schema does not list.
func TestKeysListsEveryKeyConstant(t *testing.T) {
	consts := declared(t)
	want := map[string]string{} // value -> constant name
	for name, v := range consts {
		if strings.HasPrefix(name, "Key") {
			require.NotContainsf(t, want, v, "%s and %s share the payload key %q", name, want[v], v)
			want[v] = name
		}
	}
	got := Keys()
	seen := map[string]bool{}
	for _, k := range got {
		require.Falsef(t, seen[k], "Keys() lists %q twice", k)
		seen[k] = true
		require.Containsf(t, want, k, "Keys() lists %q which is not a Key* constant", k)
		require.Truef(t, IsKey(k), "IsKey(%q)", k)
	}
	for v, name := range want {
		require.Truef(t, seen[v], "constant %s = %q is missing from Keys()", name, v)
	}
	require.False(t, IsKey("not_a_key"))
}

func TestKeysAreSnakeCase(t *testing.T) {
	for _, k := range Keys() {
		require.Regexpf(t, `^[a-z][a-z0-9_]*$`, k, "payload key %q must be lower snake_case", k)
	}
}

// TestVocabularyValuesAreUniquePerGroup: two constants of one vocabulary group
// (Source*, SourceType*, DocKind*, Tag*, Filament*) must not spell the same
// value; that would make the two meanings indistinguishable to a reader.
func TestVocabularyValuesAreUniquePerGroup(t *testing.T) {
	groups := []string{"SourceType", "Source", "DocKind", "Tag", "Filament", "Index"}
	byGroup := map[string]map[string]string{}
	for name, v := range declared(t) {
		for _, g := range groups {
			// "SourceType" is listed before "Source" so SourceType* never lands in Source.
			if strings.HasPrefix(name, g) {
				if byGroup[g] == nil {
					byGroup[g] = map[string]string{}
				}
				if prev, dup := byGroup[g][v]; dup {
					t.Errorf("%s and %s both spell %q", prev, name, v)
				}
				byGroup[g][v] = name
				break
			}
		}
	}
}

// TestVocabularyListsMatchConstants: FitSources, SourceTypes, Filaments and Tags
// list exactly the constants of their group, so a value added as a constant but
// forgotten in the list (or the other way round) fails here.
func TestVocabularyListsMatchConstants(t *testing.T) {
	consts := declared(t)
	groups := []struct {
		name    string
		list    []string
		include func(constName string) bool
	}{
		{"FitSources", FitSources(), func(n string) bool {
			return strings.HasPrefix(n, "Source") && !strings.HasPrefix(n, "SourceType")
		}},
		{"SourceTypes", SourceTypes(), func(n string) bool { return strings.HasPrefix(n, "SourceType") }},
		{"Filaments", Filaments(), func(n string) bool { return strings.HasPrefix(n, "Filament") }},
		{"Tags", Tags(), func(n string) bool { return strings.HasPrefix(n, "Tag") && !strings.HasSuffix(n, "Prefix") }},
	}
	for _, g := range groups {
		want := map[string]string{} // value -> constant name
		for name, v := range consts {
			if g.include(name) {
				want[v] = name
			}
		}
		seen := map[string]bool{}
		for _, v := range g.list {
			require.Falsef(t, seen[v], "%s() lists %q twice", g.name, v)
			seen[v] = true
			require.Containsf(t, want, v, "%s() lists %q, which is not a constant of its group", g.name, v)
		}
		for v, name := range want {
			require.Truef(t, seen[v], "constant %s = %q is missing from %s()", name, v, g.name)
		}
	}
}

func TestIsFitTag(t *testing.T) {
	for _, tag := range []string{
		TagPvP, TagCheap, TagAlphaClone, TagAbyss, "abyss-exotic", "abyss-dark", "abyss-t1", "abyss-t6",
	} {
		require.Truef(t, IsFitTag(tag), "%q is a fit tag", tag)
	}
	for _, tag := range []string{"", "PvP", "abyss-", "abyss-t", "abyss-t7", "abyss-t12", "abyss-ice", "tag", TagAbyssPrefix} {
		require.Falsef(t, IsFitTag(tag), "%q is not a fit tag", tag)
	}
}

func TestPayloadIndexesUseDeclaredKeys(t *testing.T) {
	seen := map[string]bool{}
	for _, idx := range PayloadIndexes() {
		require.Truef(t, IsKey(idx.Key), "payload index on undeclared key %q", idx.Key)
		require.Falsef(t, seen[idx.Key], "payload index on %q declared twice", idx.Key)
		seen[idx.Key] = true
		require.Contains(t, []string{IndexKeyword, IndexInteger}, idx.Schema)
	}
	// Fresh slice each call: a caller mutating the result must not change the schema.
	a := PayloadIndexes()
	a[0].Key = "mutated"
	require.NotEqual(t, "mutated", PayloadIndexes()[0].Key)
}

// snapshot renders the schema in the golden file format: the version, then one
// NAME=value line per constant, sorted.
func snapshot(consts map[string]string) string {
	names := make([]string, 0, len(consts))
	for n := range consts {
		if n == "SchemaVersion" {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "schema_version %d\n", SchemaVersion)
	for _, n := range names {
		fmt.Fprintf(&b, "%s=%s\n", n, consts[n])
	}
	return b.String()
}

func parseSnapshot(s string) (version int, entries map[string]string) {
	entries = map[string]string{}
	for i, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if i == 0 {
			version, _ = strconv.Atoi(strings.TrimPrefix(line, "schema_version "))
			continue
		}
		name, v, _ := strings.Cut(line, "=")
		entries[name] = v
	}
	return version, entries
}

// TestSchemaMatchesGolden gives SchemaVersion teeth. testdata/schema.golden is
// the committed copy of every constant of this package:
//
//   - a constant ADDED is additive: regenerate the golden file, keep the version;
//   - a constant REMOVED or whose value CHANGED is breaking: this test fails until
//     SchemaVersion is raised, and the regenerated golden file records the new one.
//
// Regenerate with: go test ./corpus -run TestSchemaMatchesGolden -update
func TestSchemaMatchesGolden(t *testing.T) {
	current := declared(t)
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
		require.NoError(t, os.WriteFile(goldenPath, []byte(snapshot(current)), 0o644))
		return
	}
	raw, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "missing golden file; create it with: go test ./corpus -run TestSchemaMatchesGolden -update")
	goldenVersion, golden := parseSnapshot(string(raw))

	var breaking, additive []string
	for name, v := range golden {
		switch cur, ok := current[name]; {
		case !ok:
			breaking = append(breaking, fmt.Sprintf("removed %s (was %q)", name, v))
		case cur != v:
			breaking = append(breaking, fmt.Sprintf("changed %s: %q -> %q", name, v, cur))
		}
	}
	for name, v := range current {
		if _, ok := golden[name]; !ok && name != "SchemaVersion" {
			additive = append(additive, fmt.Sprintf("added %s = %q", name, v))
		}
	}
	sort.Strings(breaking)
	sort.Strings(additive)

	if goldenVersion != SchemaVersion {
		t.Fatalf("SchemaVersion is %d but %s records version %d: regenerate it with -update", SchemaVersion, goldenPath, goldenVersion)
	}
	if len(breaking) > 0 {
		t.Fatalf("breaking schema change without a SchemaVersion bump (a reader of version %d would misread points written now):\n  %s\n"+
			"raise SchemaVersion in corpus.go, then regenerate the golden file with: go test ./corpus -run TestSchemaMatchesGolden -update",
			SchemaVersion, strings.Join(breaking, "\n  "))
	}
	if len(additive) > 0 {
		t.Fatalf("additive schema change not recorded in %s:\n  %s\nregenerate it with: go test ./corpus -run TestSchemaMatchesGolden -update",
			goldenPath, strings.Join(additive, "\n  "))
	}
}
