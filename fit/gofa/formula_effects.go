package gofa

import (
	"eve-cyno.dev/go/data/fit"
	"slices"
	"strconv"

	"eve-cyno.dev/go/data/dogma"
)

// Effects the SDE carries without modifierInfo.
//
// Two kinds of module effect have no modifierInfo and still matter to a fit:
//   - effects whose result a Go formula step computes (propulsion, missile and drone damage,
//     weapon damage, repair, capacitor): listed in effectHandling with the code that handles them;
//   - effects the game computes but this engine does not model: not listed, so unmodelledEffects
//     reports them in FitStats.Unmodelled as "effect:<id>", never silently ignored.
//
// formulaEffectModifiers is the third case: an effect whose result is a plain modifier set the SDE
// left out. The cached SDE returns these rows when the SDE has none.

// UnmodelledEffectPrefix starts the FitStats.Unmodelled entry of an active module effect that
// has no modifiers and no formula step.
const UnmodelledEffectPrefix = "effect:"

const (
	attrArmorEMResonance        = 267
	attrArmorExplosiveResonance = 268
	attrArmorKineticResonance   = 269
	attrArmorThermalResonance   = 270
)

// formulaEffectModifiers holds synthetic modifiers for effects whose modifierInfo is empty.
//
// 4928 adaptiveArmorHardener (Reactive Armor Hardener): in game the module shifts its four
// resonances toward the damage it takes. The fitting window shows its default, unshifted state
// (0.85 on all four, the module's own attributes 267-270) against a uniform profile, and this
// engine models exactly that static state; the adaptive shift is NOT modelled. Operation PreMul puts
// the rows into the same stacking pool as the Damage Control (EVE University wiki, "Armor
// tanking": "Only a Reactive Armor Hardener is stacking penalized with a damage control." and
// "This module is not stacking penalized with other modules except for Damage Control.",
// https://wiki.eveuniversity.org/Armor_tanking). An offline module is already Inert; a module
// that cannot cycle is Idle (online, not active), any other online module counts as active.
var formulaEffectModifiers = map[int][]dogma.Modifier{
	4928: {
		{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: attrArmorEMResonance, ModifyingAttr: attrArmorEMResonance, Operation: opPreMul},
		{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: attrArmorExplosiveResonance, ModifyingAttr: attrArmorExplosiveResonance, Operation: opPreMul},
		{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: attrArmorKineticResonance, ModifyingAttr: attrArmorKineticResonance, Operation: opPreMul},
		{Domain: "shipID", Func: "ItemModifier", ModifiedAttr: attrArmorThermalResonance, ModifyingAttr: attrArmorThermalResonance, Operation: opPreMul},
	},
}

// effectHandling lists every effect that has no modifierInfo, no entry in formulaEffectModifiers,
// and needs no unmodelled flag, with the reason. effects_guard_test.go checks the list against the
// real SDE: a new such effect on a fittable module fails the test until it is classified here or
// modelled.
var effectHandling = func() map[int]string {
	m := map[int]string{}
	add := func(why string, ids ...int) {
		for _, id := range ids {
			m[id] = why
		}
	}
	add("propulsion formula (applyPropulsionModules)", 6730, 6731)
	add("weapon damage read from the charge or module attributes (computeDPS)", 101, 10, 34, 263)
	add("repair and boost read from module attributes (repairSummary, capacitor)", 4, 26, 27, 4936, 5275, 48)
	add("online flag only, no stat", 16)
	add("acts on another ship, no stat on the fit: remote assistance and electronic warfare",
		55, 54, 6184, 6185, 6186, 6187, 6188, 6197, 6422, 6423, 6424, 6425, 6426, 6427, 6428, 6470,
		6651, 6652, 6687, 6688, 6689, 6690, 6691, 6692, 6693, 6694, 6695, 7166)
	add("no fit stat: Breach Control (12916) and the Tactical Capsuleer Recloner (8093) change nothing the stats read", 12916, 8093)
	add("not a combat-fit stat: scanning, mining, salvaging, hacking, tractor, compression, entosis",
		17, 46, 47, 67, 1738, 2255, 2726, 2757, 5163, 6063, 8364)
	return m
}()

// unmodelledAffects narrows the blocks an unmodelled effect can change; an effect not listed may change any block.
// The micro jump drive acts on the ship's position and the capacitor, never on damage or tank.
var unmodelledAffects = map[int][]fit.StatBlock{
	4921: {fit.BlockNavigation, fit.BlockCapacitor},
}

// unmodelledEffects returns the "effect:<id>" entries of the online modules of items that carry an
// active, target or overload-category effect with no modifiers, no formula step and no
// classification in effectHandling. Sorted, without duplicates.
func unmodelledEffects(s SDE, items []*Item) []string {
	seen := map[int]bool{}
	for _, it := range items {
		if it.Inert || it.Kind != KindModule {
			continue
		}
		for _, eff := range s.GetTypeEffectIDs(it.TypeID) {
			if seen[eff] || len(s.GetEffectModifiers(eff)) > 0 {
				continue
			}
			if _, ok := effectHandling[eff]; ok {
				continue
			}
			if it.Idle && !effectActs(s.GetEffectCategory(eff), it) {
				continue // an idle module's active effects do not act, so none is unmodelled
			}
			switch s.GetEffectCategory(eff) {
			case 1, 2, 3: // active, target, area
				seen[eff] = true
			}
		}
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = UnmodelledEffectPrefix + strconv.Itoa(id)
	}
	return out
}
