package intelbase

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// Stat is the mean and the sample standard deviation of one metric of one hour of
// the week, over the N collected hours that fed it.
//
// N counts the hours the collector sampled for that hour of the week; an hour it
// missed is missing, never zero. SD is 0 when N < 2 (one sample has no spread): use
// HasSD to tell that apart from a spread of exactly 0.
type Stat struct {
	Mean, SD float64
	N        int
}

// HasSD reports whether SD is a real sample deviation (at least two samples).
func (s Stat) HasSD() bool { return s.N >= 2 }

// errCorrupt marks sums that cannot come from any set of non-negative counts
// (a negative n, or a variance numerator below zero). The reader refuses such a
// row instead of returning a wrong number.
var errCorrupt = errors.New("intelbase: inconsistent baseline sums")

// newStat builds the Stat of n samples with the given sum and sum of squares.
//
//	mean     = sum / n
//	variance = (n*sumsq - sum*sum) / (n*(n-1))      (sample variance, n >= 2)
//
// The numerator is computed exactly in 128 bits. Overflow bounds: n and sumsq are
// non-negative int64 and |sum| is below 2^63, so n*sumsq and sum*sum are each below
// 2^126 and never overflow; the difference is exact for every int64 input. In plain
// int64 the numerator would overflow once n*sumsq passes 9.2e18, that is at n*x of
// about 3e9 for samples of size x (520 weekly samples of 5.8 million jumps): far
// outside real data, but the reader does not depend on that. The one float64
// conversion at the end rounds the numerator once (relative error about 1e-16).
func newStat(sum, sumsq, n int64) (Stat, error) {
	if n < 0 || sumsq < 0 {
		return Stat{}, fmt.Errorf("%w: n %d sumsq %d", errCorrupt, n, sumsq)
	}
	if n == 0 {
		if sum != 0 || sumsq != 0 {
			return Stat{}, fmt.Errorf("%w: sums %d %d without samples", errCorrupt, sum, sumsq)
		}
		return Stat{}, nil
	}
	st := Stat{Mean: float64(sum) / float64(n), N: int(n)}
	if n < 2 {
		return st, nil
	}
	abs := uint64(sum)
	if sum < 0 {
		abs = -uint64(sum)
	}
	aHi, aLo := bits.Mul64(uint64(n), uint64(sumsq))
	bHi, bLo := bits.Mul64(abs, abs)
	lo, borrow := bits.Sub64(aLo, bLo, 0)
	hi, borrow := bits.Sub64(aHi, bHi, borrow)
	if borrow != 0 {
		return Stat{}, fmt.Errorf("%w: sum %d sumsq %d n %d", errCorrupt, sum, sumsq, n)
	}
	num := float64(hi)*(1<<64) + float64(lo)
	st.SD = math.Sqrt(num / (float64(n) * float64(n-1)))
	return st, nil
}
