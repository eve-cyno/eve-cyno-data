package intelsnap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var pinned = map[string]string{
	"intel-gates-now.v1.golden.json":            "6fc7106ad30156714b6c0876c82b80a8a9a99616c11075df1d26ebad6e327b73",
	"intel-systems-hour.v1.golden.json":         "21d78a90b05510d8e3c32a68d5eb28d5b21cdd3b1a2979239708904840df1a86",
	"intel-gates-profile.v1.golden.json":        "0b33e0ce984e974f2657109be85d09fd2877cf2f7818ced859fbcac45e8017ca",
	"intel-gates-now.v1.empty.golden.json":      "33fd24d9f430cda315d4ac5432363b949ac67c8b72df89a40259dcc5dc4b2256",
	"intel-gates-now.v1.trimmed.golden.json":    "723e64b258ece1c25f97aa5c43bf4b2204fd860a8fe64326496c5fd8b62a53d9",
	"intel-systems-hour.v1.partial.golden.json": "1ac420022680707c70f9177a6a6b4234e658f6ec989f8b99605fcfc3b0371bd3",
	"intel-gates-profile.v1.empty.golden.json":  "63b92d04bc788a712f910a6e9bbbf5f18820b249811ebf37e87d02a7fb4b7a40",
}

func golden(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func strict(t *testing.T, b []byte, v any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	require.NoError(t, d.Decode(v), "strict decode")
}

func TestGolden_SHA256Pinned(t *testing.T) {
	for name, want := range pinned {
		sum := sha256.Sum256(golden(t, name))
		require.Equal(t, want, hex.EncodeToString(sum[:]), name)
	}
}

func TestDecode_Goldens(t *testing.T) {
	for name := range pinned {
		b := golden(t, name)
		switch {
		case strings.HasPrefix(name, "intel-gates-now"):
			var s GatesNow
			strict(t, b, &s)
			_, err := DecodeGatesNow(b)
			require.NoError(t, err, name)
		case strings.HasPrefix(name, "intel-systems-hour"):
			var s SystemsHour
			strict(t, b, &s)
			_, err := DecodeSystemsHour(b)
			require.NoError(t, err, name)
		default:
			var s GatesProfile
			strict(t, b, &s)
			_, err := DecodeGatesProfile(b)
			require.NoError(t, err, name)
		}
	}

	g, err := DecodeGatesNow(golden(t, "intel-gates-now.v1.golden.json"))
	require.NoError(t, err)
	require.NotEmpty(t, g.Credit)
	require.Equal(t, 1, g.Version)
	require.False(t, g.Trimmed)
	require.NotEmpty(t, g.Gates)
	require.NotEmpty(t, g.Systems)
	require.NotNil(t, g.Feed.NewestKillmailTime)
	require.NotNil(t, g.Feed.DelayP50S)
	require.NotNil(t, g.Feed.DelayP90S)
	require.NotZero(t, g.Feed.DelaySamples)
	require.Equal(t, 2, g.Gates[0].StructureKills60m)

	e, err := DecodeGatesNow(golden(t, "intel-gates-now.v1.empty.golden.json"))
	require.NoError(t, err)
	require.Nil(t, e.Feed.NewestKillmailTime)
	require.Nil(t, e.Feed.DelayP50S)
	require.Nil(t, e.Feed.DelayP90S)

	tr, err := DecodeGatesNow(golden(t, "intel-gates-now.v1.trimmed.golden.json"))
	require.NoError(t, err)
	require.True(t, tr.Trimmed)

	sh, err := DecodeSystemsHour(golden(t, "intel-systems-hour.v1.golden.json"))
	require.NoError(t, err)
	require.NotEmpty(t, sh.Credit)
	require.True(t, sh.Collected.Jumps)
	require.True(t, sh.Collected.Kills)
	require.NotEmpty(t, sh.Systems)

	sp, err := DecodeSystemsHour(golden(t, "intel-systems-hour.v1.partial.golden.json"))
	require.NoError(t, err)
	require.False(t, sp.Collected.Kills)

	p, err := DecodeGatesProfile(golden(t, "intel-gates-profile.v1.golden.json"))
	require.NoError(t, err)
	require.NotEmpty(t, p.Credit)
	require.NotZero(t, p.HistoryDays)
	require.NotEmpty(t, p.Gates)

	pe, err := DecodeGatesProfile(golden(t, "intel-gates-profile.v1.empty.golden.json"))
	require.NoError(t, err)
	require.Zero(t, pe.HistoryDays)
	require.False(t, pe.Trimmed)
}

// The mirror types marshal back to the exact bytes of every golden.
func TestGolden_RoundTrip(t *testing.T) {
	for name := range pinned {
		b := golden(t, name)
		var v any
		switch {
		case strings.HasPrefix(name, "intel-gates-now"):
			v = new(GatesNow)
		case strings.HasPrefix(name, "intel-systems-hour"):
			v = new(SystemsHour)
		default:
			v = new(GatesProfile)
		}
		require.NoError(t, json.Unmarshal(b, v), name)
		out, err := json.Marshal(v)
		require.NoError(t, err, name)
		require.Equal(t, string(bytes.TrimSpace(b)), string(out), name)
	}
}

func TestDecode_UnknownMajor(t *testing.T) {
	cases := map[string]func([]byte) error{
		"intel-gates-now":     func(b []byte) error { _, err := DecodeGatesNow(b); return err },
		"intel-systems-hour":  func(b []byte) error { _, err := DecodeSystemsHour(b); return err },
		"intel-gates-profile": func(b []byte) error { _, err := DecodeGatesProfile(b); return err },
	}
	for format, dec := range cases {
		err := dec([]byte(`{"format":"` + format + `","version":2}`))
		var fe *FormatError
		require.ErrorAs(t, err, &fe, format)
		require.Equal(t, format, fe.Format)
		require.Equal(t, 2, fe.Version)
		require.Contains(t, err.Error(), "newer format")
	}
}

// A malformed version is a bad file, not a newer format.
func TestDecode_InvalidVersionIsNotNewerFormat(t *testing.T) {
	cases := map[string]func([]byte) error{
		"intel-gates-now":     func(b []byte) error { _, err := DecodeGatesNow(b); return err },
		"intel-systems-hour":  func(b []byte) error { _, err := DecodeSystemsHour(b); return err },
		"intel-gates-profile": func(b []byte) error { _, err := DecodeGatesProfile(b); return err },
	}
	for format, dec := range cases {
		for name, body := range map[string]string{
			"zero":     `{"format":"` + format + `","version":0}`,
			"missing":  `{"format":"` + format + `"}`,
			"negative": `{"format":"` + format + `","version":-1}`,
		} {
			err := dec([]byte(body))
			require.Error(t, err, format+" "+name)
			var fe *FormatError
			require.False(t, errors.As(err, &fe), "%s %s: got *FormatError: %v", format, name, err)
			require.NotContains(t, err.Error(), "newer format")
		}
	}
}

func TestDecode_UnknownFieldIgnored(t *testing.T) {
	g, err := DecodeGatesNow([]byte(`{"format":"intel-gates-now","version":1,"future":{"a":1},"gates":[{"system_id":5,"extra":true}]}`))
	require.NoError(t, err)
	require.Len(t, g.Gates, 1)
	require.EqualValues(t, 5, g.Gates[0].SystemID)
}

func TestDecode_WrongFormat(t *testing.T) {
	_, err := DecodeGatesNow([]byte(`{"format":"intel-systems-hour","version":1}`))
	var fe *FormatError
	require.Error(t, err)
	require.False(t, errors.As(err, &fe))
	_, err = DecodeSystemsHour([]byte(`{"version":1}`))
	require.Error(t, err, "missing format accepted")
	_, err = DecodeGatesProfile([]byte(`not json`))
	require.Error(t, err, "garbage accepted")
}

func put(t testing.TB, dir, name string, b []byte) {
	t.Helper()
	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Error(err)
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		t.Error(err)
	}
}

func TestReader_RereadsOnChange(t *testing.T) {
	dir := t.TempDir()
	r := NewReader(dir)
	p := filepath.Join(dir, "intel-gates-now.json")
	put(t, dir, "intel-gates-now.json", golden(t, "intel-gates-now.v1.golden.json"))
	a, err := r.GatesNow()
	require.NoError(t, err)
	b, err := r.GatesNow()
	require.NoError(t, err)
	require.Same(t, a, b, "unchanged file must return the cached value")

	// replaced file: new inode, other size
	put(t, dir, "intel-gates-now.json", golden(t, "intel-gates-now.v1.empty.golden.json"))
	c, err := r.GatesNow()
	require.NoError(t, err)
	require.NotSame(t, a, c)
	require.Empty(t, c.Gates)

	// same size, same inode, new mtime
	require.NoError(t, os.Chtimes(p, time.Now(), time.Now().Add(time.Hour)))
	d, err := r.GatesNow()
	require.NoError(t, err)
	require.NotSame(t, c, d, "mtime change not re-read")

	// a bad file reports an error
	put(t, dir, "intel-gates-now.json", []byte(`{"format":"intel-gates-now","version":2}`))
	var fe *FormatError
	_, err = r.GatesNow()
	require.ErrorAs(t, err, &fe)

	put(t, dir, "intel-systems-hour.json", golden(t, "intel-systems-hour.v1.golden.json"))
	put(t, dir, "intel-gates-profile.json", golden(t, "intel-gates-profile.v1.golden.json"))
	s, err := r.SystemsHour()
	require.NoError(t, err)
	require.NotEmpty(t, s.Systems)
	gp, err := r.GatesProfile()
	require.NoError(t, err)
	require.NotEmpty(t, gp.Gates)
}

// Only the inode changes: same size, same modification time.
func TestReader_RereadsOnInodeChangeOnly(t *testing.T) {
	dir := t.TempDir()
	r := NewReader(dir)
	p := filepath.Join(dir, "intel-gates-now.json")
	orig := golden(t, "intel-gates-now.v1.golden.json")
	require.True(t, bytes.Contains(orig, []byte(`"state":"fight"`)))
	put(t, dir, "intel-gates-now.json", orig)
	fi, err := os.Stat(p)
	require.NoError(t, err)
	a, err := r.GatesNow()
	require.NoError(t, err)
	require.Equal(t, "fight", a.Gates[0].State)

	other := bytes.Replace(orig, []byte(`"state":"fight"`), []byte(`"state":"quiet"`), 1)
	require.Len(t, other, len(orig))
	put(t, dir, "intel-gates-now.json", other)
	require.NoError(t, os.Chtimes(p, fi.ModTime(), fi.ModTime()))
	fi2, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, fi.Size(), fi2.Size())
	require.True(t, fi.ModTime().Equal(fi2.ModTime()))
	require.False(t, os.SameFile(fi, fi2), "test needs a new inode")

	b, err := r.GatesNow()
	require.NoError(t, err)
	require.Equal(t, "quiet", b.Gates[0].State)
}

// A bad file is decoded once; a changed file is read again.
func TestReader_CachesDecodeError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "intel-gates-now.json")
	put(t, dir, "intel-gates-now.json", []byte(`{"format":"intel-gates-now","version":2}`))
	var c cached[GatesNow]
	reads := 0
	dec := func(b []byte) (*GatesNow, error) { reads++; return DecodeGatesNow(b) }

	var fe *FormatError
	_, err1 := load(&c, p, dec)
	require.ErrorAs(t, err1, &fe)
	_, err2 := load(&c, p, dec)
	require.ErrorAs(t, err2, &fe)
	_, err3 := load(&c, p, dec)
	require.ErrorAs(t, err3, &fe)
	require.Equal(t, 1, reads, "unchanged bad file must be decoded once")

	put(t, dir, "intel-gates-now.json", golden(t, "intel-gates-now.v1.golden.json"))
	g, err := load(&c, p, dec)
	require.NoError(t, err)
	require.NotEmpty(t, g.Gates)
	require.Equal(t, 2, reads)
}

func TestReader_MissingFile(t *testing.T) {
	r := NewReader(t.TempDir())
	_, err := r.GatesNow()
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = r.SystemsHour()
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = r.GatesProfile()
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestReader_ConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	full, empty := golden(t, "intel-gates-now.v1.golden.json"), golden(t, "intel-gates-now.v1.empty.golden.json")
	put(t, dir, "intel-gates-now.json", full)
	r := NewReader(dir)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				put(t, dir, "intel-gates-now.json", empty)
			} else {
				put(t, dir, "intel-gates-now.json", full)
			}
		}
	}()
	var rw sync.WaitGroup
	for i := 0; i < 8; i++ {
		rw.Add(1)
		go func() {
			defer rw.Done()
			for j := 0; j < 300; j++ {
				g, err := r.GatesNow()
				if err != nil {
					t.Error(err)
					return
				}
				_ = len(g.Gates)
			}
		}()
	}
	rw.Wait()
	close(stop)
	wg.Wait()
}

func TestReader_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	out := t.TempDir()
	put(t, out, "target.json", golden(t, "intel-gates-now.v1.golden.json"))
	require.NoError(t, os.Symlink(filepath.Join(out, "target.json"), filepath.Join(dir, "intel-gates-now.json")))
	_, err := NewReader(dir).GatesNow()
	require.ErrorIs(t, err, ErrNotRegular, "a symlinked snapshot was followed")
	require.False(t, errors.Is(err, os.ErrNotExist))
}

// Lstat sees the symlink as its own inode, so the cache key never matches it; this guards against
// the reader switching to os.Stat, which would follow the link to the cached inode. The symlink check must
// run before the cache is consulted.
func TestReader_RefusesSymlinkSwappedInAfterACachedRead(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "intel-gates-now.json")
	put(t, dir, "intel-gates-now.json", golden(t, "intel-gates-now.v1.golden.json"))
	r := NewReader(dir)
	_, err := r.GatesNow()
	require.NoError(t, err)
	require.NoError(t, os.Rename(f, f+".real"))
	require.NoError(t, os.Symlink(f+".real", f))
	_, err = r.GatesNow()
	require.ErrorIs(t, err, ErrNotRegular)
}

func TestReader_RefusesOversizeAndCachesTheError(t *testing.T) {
	dir := t.TempDir()
	r := NewReader(dir)
	put(t, dir, "intel-gates-now.json", bytes.Repeat([]byte("x"), MaxFileBytes+1))
	_, err1 := r.GatesNow()
	require.Error(t, err1)
	require.ErrorIs(t, err1, ErrTooLarge)
	_, err2 := r.GatesNow()
	require.Same(t, err1, err2, "an unchanged oversize file must return the cached error")

	// exactly at the bound is read (and fails to decode, not for its size)
	put(t, dir, "intel-gates-now.json", bytes.Repeat([]byte("x"), MaxFileBytes))
	_, err := r.GatesNow()
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTooLarge)
}

// A symlink swapped in between the Lstat and the open fails the open with ELOOP; the reader
// names it ErrNotRegular. The race itself is not deterministic, so the mapping is tested alone.
func TestOpenFailure_NamesASymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "l.json")
	require.NoError(t, os.Symlink(filepath.Join(dir, "elsewhere"), link))
	raw := errors.New("too many levels of symbolic links")
	require.ErrorIs(t, openFailure(link, raw), ErrNotRegular)
	require.Same(t, raw, openFailure(filepath.Join(dir, "missing"), raw))
}
