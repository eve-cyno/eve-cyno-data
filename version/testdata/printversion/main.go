// Command printversion prints what package version reports. It exists only for
// version_test.go, which builds it with the same -X flag the Dockerfiles and the
// release workflows use.
package main

import (
	"fmt"

	"eve-cyno.dev/go/data/version"
)

func main() {
	fmt.Printf("version=%s raw=%s\n", version.Version(), version.Raw())
}
