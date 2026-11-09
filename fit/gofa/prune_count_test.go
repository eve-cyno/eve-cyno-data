//go:build !long

package gofa

// pruneFits is the number of random fits of TestPrune_EqualsFull per profile. The
// plan asks for 500 by default and 2,000 under the build tag long.
const pruneFits = 500
