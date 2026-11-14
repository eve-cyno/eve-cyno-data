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
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func strict(t *testing.T, b []byte, v any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		t.Fatalf("strict decode: %v", err)
	}
}

func TestGolden_SHA256Pinned(t *testing.T) {
	for name, want := range pinned {
		sum := sha256.Sum256(golden(t, name))
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: sha256 %s, want %s", name, got, want)
		}
	}
}

func TestDecode_Goldens(t *testing.T) {
	for name := range pinned {
		b := golden(t, name)
		switch {
		case strings.HasPrefix(name, "intel-gates-now"):
			var s GatesNow
			strict(t, b, &s)
			if _, err := DecodeGatesNow(b); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case strings.HasPrefix(name, "intel-systems-hour"):
			var s SystemsHour
			strict(t, b, &s)
			if _, err := DecodeSystemsHour(b); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		default:
			var s GatesProfile
			strict(t, b, &s)
			if _, err := DecodeGatesProfile(b); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}

	g, _ := DecodeGatesNow(golden(t, "intel-gates-now.v1.golden.json"))
	if g.Credit == "" || g.Version != 1 || g.Trimmed || len(g.Gates) == 0 || len(g.Systems) == 0 {
		t.Errorf("gates-now golden: %+v", g)
	}
	if g.Feed.NewestKillmailTime == nil || g.Feed.DelayP50S == nil || g.Feed.DelayP90S == nil || g.Feed.DelaySamples == 0 {
		t.Errorf("gates-now feed: %+v", g.Feed)
	}
	if g.Gates[0].StructureKills60m != 2 {
		t.Errorf("structure_kills_60m = %d, want 2", g.Gates[0].StructureKills60m)
	}
	e, _ := DecodeGatesNow(golden(t, "intel-gates-now.v1.empty.golden.json"))
	if e.Feed.NewestKillmailTime != nil || e.Feed.DelayP50S != nil || e.Feed.DelayP90S != nil {
		t.Errorf("empty feed should hold nil pointers: %+v", e.Feed)
	}
	tr, _ := DecodeGatesNow(golden(t, "intel-gates-now.v1.trimmed.golden.json"))
	if !tr.Trimmed {
		t.Error("trimmed golden: Trimmed false")
	}
	sh, _ := DecodeSystemsHour(golden(t, "intel-systems-hour.v1.golden.json"))
	if sh.Credit == "" || !sh.Collected.Jumps || !sh.Collected.Kills || len(sh.Systems) == 0 {
		t.Errorf("systems-hour golden: %+v", sh)
	}
	sp, _ := DecodeSystemsHour(golden(t, "intel-systems-hour.v1.partial.golden.json"))
	if sp.Collected.Kills {
		t.Error("partial golden: collected.kills should be false")
	}
	p, _ := DecodeGatesProfile(golden(t, "intel-gates-profile.v1.golden.json"))
	if p.Credit == "" || p.HistoryDays == 0 || len(p.Gates) == 0 {
		t.Errorf("profile golden: %+v", p)
	}
	pe, _ := DecodeGatesProfile(golden(t, "intel-gates-profile.v1.empty.golden.json"))
	if pe.HistoryDays != 0 || pe.Trimmed {
		t.Errorf("empty profile: %+v", pe)
	}
}

func TestDecode_UnknownMajor(t *testing.T) {
	cases := map[string]func([]byte) error{
		"intel-gates-now":     func(b []byte) error { _, err := DecodeGatesNow(b); return err },
		"intel-systems-hour":  func(b []byte) error { _, err := DecodeSystemsHour(b); return err },
		"intel-gates-profile": func(b []byte) error { _, err := DecodeGatesProfile(b); return err },
	}
	for format, dec := range cases {
		for _, v := range []string{"2", "0"} {
			err := dec([]byte(`{"format":"` + format + `","version":` + v + `}`))
			var fe *FormatError
			if !errors.As(err, &fe) {
				t.Fatalf("%s v%s: want *FormatError, got %v", format, v, err)
			}
			if fe.Format != format || fe.Version == 1 {
				t.Errorf("%s v%s: %+v", format, v, fe)
			}
			if !strings.Contains(err.Error(), "newer format") {
				t.Errorf("message: %q", err)
			}
		}
	}
}

func TestDecode_UnknownFieldIgnored(t *testing.T) {
	g, err := DecodeGatesNow([]byte(`{"format":"intel-gates-now","version":1,"future":{"a":1},"gates":[{"system_id":5,"extra":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Gates) != 1 || g.Gates[0].SystemID != 5 {
		t.Errorf("%+v", g)
	}
}

func TestDecode_WrongFormat(t *testing.T) {
	_, err := DecodeGatesNow([]byte(`{"format":"intel-systems-hour","version":1}`))
	var fe *FormatError
	if err == nil || errors.As(err, &fe) {
		t.Errorf("want plain error, got %v", err)
	}
	if _, err := DecodeSystemsHour([]byte(`{"version":1}`)); err == nil {
		t.Error("missing format accepted")
	}
	if _, err := DecodeGatesProfile([]byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
}

func put(t testing.TB, dir, name string, b []byte) {
	t.Helper()
	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func TestReader_RereadsOnChange(t *testing.T) {
	dir := t.TempDir()
	r := NewReader(dir)
	put(t, dir, "intel-gates-now.json", golden(t, "intel-gates-now.v1.golden.json"))
	a, err := r.GatesNow()
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.GatesNow()
	if err != nil || a != b {
		t.Fatalf("unchanged file must return the cached value (err %v)", err)
	}
	// replaced file: new inode, other size
	put(t, dir, "intel-gates-now.json", golden(t, "intel-gates-now.v1.empty.golden.json"))
	c, err := r.GatesNow()
	if err != nil {
		t.Fatal(err)
	}
	if c == a || len(c.Gates) != 0 {
		t.Errorf("replaced file not re-read: %+v", c)
	}
	// same size, same inode, new mtime
	p := filepath.Join(dir, "intel-gates-now.json")
	if err := os.Chtimes(p, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	d, err := r.GatesNow()
	if err != nil || d == c {
		t.Errorf("mtime change not re-read (err %v)", err)
	}
	// a bad file reports an error and is not cached
	put(t, dir, "intel-gates-now.json", []byte(`{"format":"intel-gates-now","version":2}`))
	var fe *FormatError
	if _, err := r.GatesNow(); !errors.As(err, &fe) {
		t.Errorf("want FormatError, got %v", err)
	}

	put(t, dir, "intel-systems-hour.json", golden(t, "intel-systems-hour.v1.golden.json"))
	put(t, dir, "intel-gates-profile.json", golden(t, "intel-gates-profile.v1.golden.json"))
	if s, err := r.SystemsHour(); err != nil || len(s.Systems) == 0 {
		t.Errorf("systems hour: %v", err)
	}
	if s, err := r.GatesProfile(); err != nil || len(s.Gates) == 0 {
		t.Errorf("gates profile: %v", err)
	}
}

func TestReader_MissingFile(t *testing.T) {
	r := NewReader(t.TempDir())
	if _, err := r.GatesNow(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("gates now: %v", err)
	}
	if _, err := r.SystemsHour(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("systems hour: %v", err)
	}
	if _, err := r.GatesProfile(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("gates profile: %v", err)
	}
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
