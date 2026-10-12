// Command gen writes dataapi/fielddocs.json from the struct field comments of the core
// module (see package fielddocs). Run it through `go generate ./...` in the core module.
package main

import (
	"flag"
	"fmt"
	"os"

	"eve-cyno.dev/go/data/internal/fielddocs"
)

func main() {
	root := flag.String("root", ".", "the core module root")
	out := flag.String("out", "dataapi/fielddocs.json", "the file to write")
	flag.Parse()
	b, err := fielddocs.Generate(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fielddocs:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "fielddocs:", err)
		os.Exit(1)
	}
}
