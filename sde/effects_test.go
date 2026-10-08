package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetEffectModifiers_Effect21 verifies that effectID 21 (shieldCapacityBonusOnline)
// has at least one modifier with the expected field values as ingested by Task 1.
func TestGetEffectModifiers_Effect21(t *testing.T) {
	s := testSDE(t)
	defer s.Close()

	mods := s.GetEffectModifiers(21)
	require.NotNil(t, mods, "expected modifiers for effectID 21")
	require.GreaterOrEqual(t, len(mods), 1, "expected at least one modifier")

	m := mods[0]
	require.Equal(t, "shipID", m.Domain)
	require.Equal(t, "ItemModifier", m.Func)
	require.Equal(t, 263, m.ModifiedAttr)
	require.Equal(t, 72, m.ModifyingAttr)
	require.Equal(t, 2, m.Operation)
}

// TestGetEffectModifiers_Unknown verifies nil is returned for an unknown effectID.
func TestGetEffectModifiers_Unknown(t *testing.T) {
	s := testSDE(t)
	defer s.Close()

	mods := s.GetEffectModifiers(0)
	require.Nil(t, mods, "expected nil for unknown effectID")
}

// TestGetTypeEffectIDs_Rifter verifies that the Rifter (typeID 587) has at least one effect.
func TestGetTypeEffectIDs_Rifter(t *testing.T) {
	s := testSDE(t)
	defer s.Close()

	ids := s.GetTypeEffectIDs(587)
	require.NotEmpty(t, ids, "Rifter (typeID 587) should have at least one effectID")
}

// TestGetTypeEffectIDs_Unknown verifies nil/empty is returned for an unknown typeID.
func TestGetTypeEffectIDs_Unknown(t *testing.T) {
	s := testSDE(t)
	defer s.Close()

	ids := s.GetTypeEffectIDs(0)
	require.Empty(t, ids, "expected empty result for unknown typeID")
}

// TestGetAttributeMeta_StackingPenaltySubject verifies that attrID 109
// (kineticDamageResonance, stackable=0) is correctly reported as Stackable=false,
// meaning it IS subject to the stacking penalty.
func TestGetAttributeMeta_StackingPenaltySubject(t *testing.T) {
	s := testSDE(t)
	defer s.Close()

	// attributeID 109 = kineticDamageResonance; stackable=0 in dgmAttributeTypes.
	// stackable column 0 → Stackable bool false (subject to stacking penalty).
	meta := s.GetAttributeMeta(109)
	require.False(t, meta.Stackable, "kineticDamageResonance (attrID 109) should NOT be stackable (stackable=0)")
}

// TestGetAttributeMeta_StackingPenaltyExempt verifies that attrID 9
// (hp, stackable=1) is correctly reported as Stackable=true,
// meaning it is EXEMPT from the stacking penalty.
func TestGetAttributeMeta_StackingPenaltyExempt(t *testing.T) {
	s := testSDE(t)
	defer s.Close()

	// attributeID 9 = hp; stackable=1 in dgmAttributeTypes.
	// stackable column 1 → Stackable bool true (exempt from stacking penalty).
	meta := s.GetAttributeMeta(9)
	require.True(t, meta.Stackable, "hp (attrID 9) should be stackable (stackable=1)")
}
