package dataapi_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/dataapi"
)

// accountGoldens are the account pages in three states, compared with testdata/golden. The page carries a random
// csrf token, form token, key secret and key id; the golden replaces each with a fixed text. Rewrite them with
//
//	go test ./dataapi/ -run TestPortal_AccountPagesGolden -update
//
// (never with the docs golden test, which would rewrite the docs goldens too).
var accountGoldenNormalizers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`/assets/[0-9a-f]{12}/`), "/assets/000000000000/"},
	{regexp.MustCompile(`(name="csrf" value=")[A-Za-z0-9_-]{43}`), "${1}csrf-token-sample"},
	{regexp.MustCompile(`(name="form" value=")[A-Za-z0-9_-]{43}`), "${1}form-token-sample"},
	{regexp.MustCompile(`ecd_[0-9a-f]{64}`), "ecd_EXAMPLE_NOT_A_REAL_KEY"},
	{regexp.MustCompile(`key_[0-9a-f]{12}`), "key_000000000000"},
}

func normalizeAccountPage(s string) string {
	for _, n := range accountGoldenNormalizers {
		s = n.re.ReplaceAllString(s, n.with)
	}
	return s
}

func TestPortal_AccountPagesGolden(t *testing.T) {
	f := newAcctFix(t, func(c *dataapi.Config) { c.Docs = true })
	out := f.browser()
	signedOutPage, _ := acctPage(t, out)

	b := f.browser()
	f.signIn(t, b)
	_, v := acctPage(t, b)
	secret := b.do(http.MethodPost, "/account/keys", with(v, "name", "Fit script"))
	require.Equal(t, http.StatusOK, secret.Code, secret.Body.String())
	signedInPage, _ := acctPage(t, b)

	pages := map[string]string{
		"account-signed-out.html": signedOutPage,
		"account-signed-in.html":  signedInPage,
		"account-secret.html":     secret.Body.String(),
	}
	for file, got := range pages {
		got = normalizeAccountPage(got)
		golden := filepath.Join("testdata", "golden", file)
		if *updateGolden {
			require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
			continue
		}
		want, err := os.ReadFile(golden)
		require.NoError(t, err, "golden %s (run with -update to create it)", file)
		require.True(t, got == string(want), "%s differs from its golden (a deliberate change: rerun with -update and read the diff)%s", file, firstDiff(got, string(want)))
	}
}

// Each account page carries the Fenris Creations notice, loads only its own origin's scripts and styles and runs no
// inline script, so the portal CSP (script-src 'self', style-src 'self' and the fonts host) holds.
func TestPortal_AccountPagesHonourTheCSP(t *testing.T) {
	f := newAcctFix(t, func(c *dataapi.Config) { c.Docs = true })
	home := do(f.api, http.MethodGet, "/", "").Body.String()
	legal := regexp.MustCompile(`(?s)<p class="legal">.*?</p>`).FindString(home)
	require.Contains(t, legal, "Fenris Creations")

	out := f.browser()
	signedOutPage, _ := acctPage(t, out)
	b := f.browser()
	f.signIn(t, b)
	_, v := acctPage(t, b)
	secret := b.do(http.MethodPost, "/account/keys", with(v, "name", "x"))
	signedInPage, _ := acctPage(t, b)

	scriptRE := regexp.MustCompile(`<script[^>]*>`)
	for name, page := range map[string]string{"signed-out": signedOutPage, "signed-in": signedInPage, "secret": secret.Body.String()} {
		require.Contains(t, page, legal, name)
		require.NotContains(t, page, "<style", name)
		for _, tag := range scriptRE.FindAllString(page, -1) {
			require.Regexp(t, `src="/assets/[0-9a-f]{12}/[a-z.-]+\.js"`, tag, "%s: only scripts from the portal origin", name)
		}
		require.Equal(t, 1, strings.Count(page, "<main "), name)
		for _, m := range regexp.MustCompile(`<img [^>]*src="([^"]+)"`).FindAllStringSubmatch(page, -1) {
			require.Regexp(t, `^/assets/[0-9a-f]{12}/eve-sso-login-white-large\.png$`, m[1], "%s: img-src 'self'", name)
		}
	}
	require.Contains(t, signedOutPage, `eve-sso-login-white-large.png`)
	require.Contains(t, secret.Body.String(), "data-secret-box")
}

// The self-hosted EVE sign-in button is served as a PNG from the assets and carries no signing block or other
// metadata (the design file arrives with a C2PA caBX chunk; the portal copy is stripped). The chunks are walked, not
// the bytes searched: compressed image data may hold any byte string, and an unknown chunk type must fail too.
func TestPortal_SSOButtonIsServedWithoutMetadata(t *testing.T) {
	rec := do(portalAPI(t), http.MethodGet, "/assets/000000000000/eve-sso-login-white-large.png", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	types, err := pngChunkTypes(rec.Body.Bytes())
	require.NoError(t, err)
	require.Equal(t, []string{"IHDR", "PLTE", "tRNS", "IDAT", "IEND"}, types, "image chunks only")

	// the walk itself: an inserted chunk is seen, a bad checksum or trailing bytes are an error
	png := rec.Body.Bytes()
	iend := len(png) - 12
	doctored := append(append(append([]byte{}, png[:iend]...), pngChunk("caBX", []byte("jumb c2pa"))...), png[iend:]...)
	types, err = pngChunkTypes(doctored)
	require.NoError(t, err)
	require.Equal(t, []string{"IHDR", "PLTE", "tRNS", "IDAT", "caBX", "IEND"}, types)
	bad := append([]byte{}, png...)
	bad[len(bad)-1] ^= 0xff
	_, err = pngChunkTypes(bad)
	require.Error(t, err, "a checksum that does not match")
	_, err = pngChunkTypes(append(append([]byte{}, png...), pngChunk("tEXt", []byte("after"))...))
	require.Error(t, err, "a chunk after IEND")
}

// pngChunkTypes walks a PNG's chunks (length, type, data, CRC-32 of type and data) and returns their types in order;
// a bad signature or checksum, a chunk past the end, a first chunk that is not IHDR or bytes after IEND are errors.
func pngChunkTypes(b []byte) ([]string, error) {
	const sig = "\x89PNG\r\n\x1a\n"
	if len(b) < len(sig) || string(b[:len(sig)]) != sig {
		return nil, errors.New("not a PNG")
	}
	var types []string
	for i := len(sig); i < len(b); {
		if len(b)-i < 12 {
			return nil, fmt.Errorf("truncated chunk at %d", i)
		}
		n := int(binary.BigEndian.Uint32(b[i:]))
		if n > len(b)-i-12 {
			return nil, fmt.Errorf("chunk at %d runs past the end", i)
		}
		typ, body := b[i+4:i+8], b[i+4:i+8+n]
		if crc32.ChecksumIEEE(body) != binary.BigEndian.Uint32(b[i+8+n:]) {
			return nil, fmt.Errorf("chunk %q at %d: bad checksum", typ, i)
		}
		types = append(types, string(typ))
		i += 12 + n
		if string(typ) == "IEND" && i != len(b) {
			return nil, errors.New("bytes after IEND")
		}
	}
	if len(types) == 0 || types[0] != "IHDR" {
		return nil, errors.New("the first chunk is not IHDR")
	}
	return types, nil
}

// pngChunk builds one chunk with its checksum.
func pngChunk(typ string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, typ...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[4:]))
}

// The account pages load the portal assets (styles, scripts, the EVE button), so the assets are served whenever the
// sign-in is, with the documentation off too.
func TestPortal_AccountAssetsServedWithoutDocs(t *testing.T) {
	f := newAcctFix(t) // Docs off
	body, _ := acctPage(t, f.browser())
	refs := regexp.MustCompile(`(?:src|href)="(/assets/[0-9a-f]{12}/[^"]+)"`).FindAllStringSubmatch(body, -1)
	require.NotEmpty(t, refs)
	for _, m := range refs {
		rec := do(f.api, http.MethodGet, m[1], "")
		require.Equal(t, http.StatusOK, rec.Code, m[1])
		require.NotEmpty(t, rec.Body.Bytes(), m[1])
	}
	require.Equal(t, http.StatusNotFound, do(f.api, http.MethodGet, "/docs/", "").Code, "the docs stay off")
}
