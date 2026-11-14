package dataapi_test

import (
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "rewrite dataapi/testdata/golden/docs-*.html from the current templates")

// assetVersionRE is the content-derived asset prefix; it changes whenever any file under
// portal/static does, so the goldens normalize it.
var assetVersionRE = regexp.MustCompile(`/assets/[0-9a-f]{12}/`)

// docsGoldens maps every docs path of the portal to its golden file.
var docsGoldens = map[string]string{
	"/docs/":               "docs-index.html",
	"/docs/claude-code":    "docs-claude-code.html",
	"/docs/mcp":            "docs-mcp.html",
	"/docs/rest":           "docs-rest.html",
	"/docs/tools":          "docs-tools.html",
	"/docs/access":         "docs-access.html",
	"/docs/attribution":    "docs-attribution.html",
	"/docs/terms":          "docs-terms.html",
	"/docs/tools/get_fits": "docs-tools-get_fits.html",
}

// TestPortal_DocsPagesUnchanged renders the nine docs pages (the fixed config of portalAPI,
// no live data) and compares each with its golden in testdata/golden. The goldens were taken
// from the docs pages as they were before the new home page, so a change to the shared
// partials or assets that touches a docs page fails here.
//
// A deliberate docs change (a new route, field or tool row, for example from another lane)
// is made by running the test with -update and reading the diff of the goldens:
//
//	go test ./dataapi/ -run TestPortal_DocsPagesUnchanged -update
//
// Rule between lanes: the second lane to merge runs it with -update and checks that the
// only diff is the other lane's change.
func TestPortal_DocsPagesUnchanged(t *testing.T) {
	api := portalAPI(t)
	for path, file := range docsGoldens {
		rec := do(api, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code, path)
		got := assetVersionRE.ReplaceAllString(rec.Body.String(), "/assets/000000000000/")
		golden := filepath.Join("testdata", "golden", file)
		if *updateGolden {
			require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
			require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
			continue
		}
		want, err := os.ReadFile(golden)
		require.NoError(t, err, "golden for %s (run with -update to create it)", path)
		require.True(t, got == string(want), "%s differs from %s (a deliberate docs change: rerun with -update and read the diff)%s", path, file, firstDiff(got, string(want)))
	}
}

// firstDiff names the first differing line of two texts, for a readable failure.
func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) && i < len(w); i++ {
		if g[i] != w[i] {
			return "\nline " + strconv.Itoa(i+1) + "\n got: " + g[i] + "\nwant: " + w[i]
		}
	}
	return "\nline counts differ: got " + strconv.Itoa(len(g)) + ", want " + strconv.Itoa(len(w))
}
