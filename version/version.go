// Package version reports the build / deploy version of an EVE-Cyno binary
// (core-api GET /health and /v1/stats, the native status bar, the static-asset
// cache-bust token). It is a leaf with no in-repo imports so the web host, the
// desktop app and the brain can all stamp the same value without importing each
// other.
//
// Where the value comes from, highest precedence first:
//
//  1. the API_VERSION environment variable, set per deploy to the image tag
//     (`v0.10.3`, or `sha-<7>` for a deploy by commit);
//  2. the link-time variable buildVersion, for binaries that have no runtime env
//     (the Wails desktop app) and for the Docker images, which bake their tag in;
//  3. a baked-in fallback for plain `go run` / `go build` development builds.
//
// Stamp a binary at link time with
//
//	-ldflags "-X eve-cyno.dev/go/data/version.buildVersion=<tag>"
//
// (the Dockerfiles take it from their VERSION build arg; release-desktop.yml passes
// the release tag). version_test.go builds a real binary with that exact flag, so a
// rename of this package or variable cannot silently break the release stamping.
package version

import (
	"os"
	"strings"
)

// buildVersion is the link-time version tag; empty in an unstamped build.
var buildVersion string

// fallback is reported by Version when neither API_VERSION nor buildVersion is set.
const fallback = "0.10.0"

// Version is the display version: API_VERSION, else the link-time tag, else the
// baked-in fallback, with one leading "v" stripped ("v0.10.3" -> "0.10.3"). It is
// read on every call, so a test can steer it with t.Setenv.
func Version() string {
	return resolve(os.Getenv("API_VERSION"), buildVersion)
}

// Raw is the deploy tag exactly as given — API_VERSION, else the link-time tag, with
// no "v" stripping — or "" when neither is set. The static-asset cache-bust token
// uses it so a new tag changes every asset URL; "" lets the caller pick its own
// development placeholder.
func Raw() string {
	if env := os.Getenv("API_VERSION"); env != "" {
		return env
	}
	return buildVersion
}

func resolve(env, build string) string {
	if v := strings.TrimPrefix(env, "v"); v != "" {
		return v
	}
	if v := strings.TrimPrefix(build, "v"); v != "" {
		return v
	}
	return fallback
}
