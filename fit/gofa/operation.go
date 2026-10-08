// Package gofa is a pure-Go dogma engine that interprets CCP modifierInfo
// modifiers to compute fit statistics. It depends on core/sde for the
// interface types only; core/fit must never import it.
package gofa

// EVE dogma operation codes. These match CCP's dgmExpressions / modifierInfo
// operation field (cross-checked against EVEShipFit/dogma-engine):
//
//	opPreAssign  (-1): assign value before other operations (mutator base-assign)
//	opPreMul      (0): multiply before other operations
//	opPreDiv      (1): divide before other operations
//	opModAdd      (2): additive bonus (e.g. capacitor capacity)
//	opModSub      (3): additive penalty
//	opPostMul     (4): multiply after other operations
//	opPostDiv     (5): divide after other operations
//	opPostPercent (6): percentage bonus, e.g. +10% (per-level module/skill bonus)
//	opPostAssign  (7): assign value after other operations
const (
	opPreAssign   = -1
	opPreMul      = 0
	opPreDiv      = 1
	opModAdd      = 2
	opModSub      = 3
	opPostMul     = 4
	opPostDiv     = 5
	opPostPercent = 6
	opPostAssign  = 7
)

// apply returns the result of a single dogma operation against base using the
// modifying value. Pre/Post variants share the same per-op math; the Pre/Post
// distinction governs ordering across operations and is handled by the
// interpreter, not here.
func apply(op int, base, modifying float64) float64 {
	switch op {
	case opModAdd:
		return base + modifying
	case opModSub:
		return base - modifying
	case opPostPercent:
		return base * (1 + modifying/100)
	case opPreMul, opPostMul:
		return base * modifying
	case opPreDiv, opPostDiv:
		return base / modifying
	case opPreAssign, opPostAssign:
		return modifying
	default:
		return base
	}
}

// isMultiplicative reports whether op is a multiplicative operation subject to
// the stacking penalty. Additive (add/sub) and assignment operations are not
// stacking-penalized.
func isMultiplicative(op int) bool {
	switch op {
	case opPreMul, opPostMul, opPostPercent, opPreDiv, opPostDiv:
		return true
	default:
		return false
	}
}
