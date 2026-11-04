// Command sdesubset writes the fit engine's slice of the SDE as JSON (the data pack the
// engine process and a WASM build read instead of the SQLite file).
//
//	go run ./cmd/sdesubset -sde data/sde/sde.sqlite -out sde-subset.json
//	go run ./cmd/sdesubset -sde data/sde/sde.sqlite -out sde-subset.json.gz
//
// A name ending in .gz is gzip-compressed. -version stamps the file with the SDE build id
// (the "<sde>.build" file next to the SDE is used when -version is empty).
package main

import (
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	coreconfig "eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/sde"
)

func main() {
	in := flag.String("sde", "", "SDE SQLite file (default: EVE_CORE_SDE_PATH)")
	out := flag.String("out", "sde-subset.json", "output file; .gz is compressed")
	version := flag.String("version", "", "SDE build id to stamp (default: contents of <sde>.build)")
	flag.Parse()
	if err := run(*in, *out, *version); err != nil {
		fmt.Fprintln(os.Stderr, "sdesubset:", err)
		os.Exit(1)
	}
}

func run(in, out, version string) error {
	if in == "" {
		in = coreconfig.Load().SDEPath
	}
	if version == "" {
		if b, err := os.ReadFile(in + ".build"); err == nil {
			version = strings.TrimSpace(string(b))
		}
	}
	s, err := sde.OpenWithReload(in, 0)
	if err != nil {
		return err
	}
	defer s.Close()
	sub, err := s.ExportSubset(version)
	if err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	var w io.Writer = f
	var gz *gzip.Writer
	if strings.HasSuffix(out, ".gz") {
		gz = gzip.NewWriter(f)
		w = gz
	}
	err = sub.WriteJSON(w)
	if gz != nil {
		if cerr := gz.Close(); err == nil {
			err = cerr
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
