package gofa

import "eve-cyno.dev/go/data/sde"

// Compile-time assertion that the concrete *sde.SDE satisfies the interface.
// It lives in a test file so the non-test package does not import the SQLite-backed sde.
var _ SDE = (*sde.SDE)(nil)
