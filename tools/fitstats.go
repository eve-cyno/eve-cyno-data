package tools

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/fit/gofa"
	"eve-cyno.dev/go/data/sde"
)

// groupPropulsionModule is the SDE groupID of afterburners and microwarpdrives.
const groupPropulsionModule = 46

// Dogma attributes that mark a charge-using weapon: a cycle time (speed, attr 51)
// and an accepted charge group (chargeGroup1, attr 604).
const (
	attrWeaponSpeed  = 51
	attrChargeGroup1 = 604
)

// StatsEngine returns the dogma engine behind the fit stat cards: the injected
// Stats engine, else one built lazily over SDE the first time it is needed and
// reused by every later call and goroutine (Engine is safe for concurrent use and
// keeps its SDE memo across calls, so a shared engine answers warm). It is nil
// when there is no SDE to build one over. Callers that need other engines to
// share its memo derive them from it (gofa.Engine.WithDefaultAmmo copies share
// the cache).
func (d *Deps) StatsEngine() *gofa.Engine {
	if d == nil {
		return nil
	}
	if d.Stats != nil {
		return d.Stats
	}
	if d.SDE == nil {
		return nil
	}
	d.statsOnce.Do(func() { d.statsLazy = gofa.New(d.SDE) })
	return d.statsLazy
}

// AlphaLegality returns the Alpha-clone legality checker: the injected Legality, else
// one built lazily over SDE the first time it is needed and reused after (the
// allowlist is a few dozen name lookups). It is nil when there is no SDE or the
// allowlist cannot be loaded; validate_fitting then reports the Alpha check as skipped.
func (d *Deps) AlphaLegality() *fit.Legality {
	if d == nil {
		return nil
	}
	if d.Legality != nil {
		return d.Legality
	}
	if d.SDE == nil || !d.SDE.Available() {
		return nil
	}
	d.legalityOnce.Do(func() {
		al, err := fit.LoadAlphaAllowlist(d.SDE)
		if err != nil {
			slog.Warn("tools: alpha legality unavailable, validate_fitting skips the Alpha check", "error", err)
			return
		}
		d.legalityLazy = fit.NewLegality(d.SDE, al)
	})
	return d.legalityLazy
}

// computeFitStats implements the compute_fit_stats tool: parse the EFT, load
// default ammo into weapons that carry none, run the dogma engine at all-V skills
// and render the stat card. Every failure is returned as text for the model (and
// the card is nil).
func computeFitStats(ctx context.Context, deps *Deps, args map[string]any) (string, *FitStatCard) {
	eft, _ := args["eft_text"].(string)
	if eft == "" {
		eft, _ = args["eft"].(string) // tolerate the short alias
	}
	if eft == "" {
		eft, _ = args["eft_block"].(string) // validate_fitting's internal alias
	}
	text, card, problem := fitStatCard(ctx, deps, eft, chargeOverrides(args["charges"]), true)
	if card == nil {
		return problem, nil
	}
	return text, card
}

// chargeOverrides coerces the optional "charges" argument (module name → charge
// name) into a string map. Anything else is ignored: the defaults apply.
func chargeOverrides(v any) map[string]string {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for module, charge := range raw {
		if name, ok := charge.(string); ok && strings.TrimSpace(name) != "" {
			out[module] = name
		}
	}
	return out
}

// validateFittingWithStats runs validate_fitting and, when the model asked
// (eft_text — the tool's own parameter) and the fit validates, appends the stat
// card (to the text, and as FitValidation.Stats). The guards call validate_fitting
// with eft_block on every EFT block of every answer and repair pass; the engine
// needs about a second per fit, so they keep the plain validator output.
func validateFittingWithStats(ctx context.Context, deps *Deps, args map[string]any) (string, *FitValidation, error) {
	eft, _ := args["eft_text"].(string)
	fromModel := eft != ""
	if eft == "" {
		eft, _ = args["eft_block"].(string)
	}
	alpha, _ := args["alpha_clone"].(bool)
	out, v, err := validateFitting(ctx, deps.Client, deps.SDE, deps.AlphaLegality(), eft, alpha)
	if err != nil || !fromModel || v == nil || !v.Valid {
		return out, v, err
	}
	if text, card, _ := fitStatCard(ctx, deps, eft, nil, false); card != nil {
		out += "\n\n" + text
		v.Stats = card
	}
	return out, v, nil
}

// fitStatCard computes the stat card for an EFT and renders it. It returns the card
// text and its typed form (built from the same values), or a nil card and a one-line
// explanation when the fit cannot be computed. withLoad adds the CPU / PG line
// (validate_fitting already prints its own).
func fitStatCard(ctx context.Context, deps *Deps, eft string, charges map[string]string, withLoad bool) (text string, card *FitStatCard, problem string) {
	s := deps.SDE
	eft = stripEFTFences(eft)
	if strings.TrimSpace(eft) == "" {
		return "", nil, "compute_fit_stats needs eft_text: the fit in EFT format, starting with [Ship Name, Fit Name]."
	}
	if s == nil || !s.Available() {
		return "", nil, "Fit stats are unavailable: the SDE is not loaded."
	}
	f, unresolved := fit.ParseEFT(eft, s)
	if f.HullName == "" {
		return "", nil, "Could not parse EFT: missing header line [Ship Name, Fit Name]."
	}
	if f.HullID == 0 || !s.IsShip(f.HullID) {
		return "", nil, fmt.Sprintf("Could not resolve ship '%s' to a type_id.", f.HullName)
	}

	eng := deps.StatsEngine() // the one warm engine: its SDE memo survives across calls
	loaded, ammo := eng.ApplyDefaultAmmo(f, charges)
	stats, err := eng.Stats(ctx, loaded, fit.StatsOpts{})
	if err != nil {
		return "", nil, fmt.Sprintf("Could not compute stats for %s: %v", f.HullName, err)
	}

	defaulted := false
	defaultLabels := map[string]bool{}
	for _, u := range ammo {
		if u.Default {
			defaulted = true
			defaultLabels[gofa.WeaponLabel(u.Module, u.Charge)] = true
		}
	}
	basis := "all-V"
	if defaulted {
		basis += ", default ammo"
	}

	total, drones := stats.DPS.Theoretical, stats.Drones.DPS // DPSStats.Theoretical already includes the drones
	card = &FitStatCard{
		Hull:              f.HullName,
		Basis:             basis,
		DPS:               total,
		WeaponDPS:         math.Max(total-drones, 0),
		DroneDPS:          drones,
		Volley:            stats.Volley,
		Weapons:           weaponGroups(stats, defaultLabels),
		Drones:            droneFlight(stats, f),
		Tank:              tankCard(stats),
		SpeedMS:           stats.Navigation.MaxVelocity,
		PropOn:            propOn(s, loaded),
		AlignS:            stats.Navigation.AlignTimeSec,
		SignatureM:        stats.Navigation.SignatureRadius,
		Cap:               capCard(stats),
		NoAmmo:            nonNil(unloadedWeapons(s, loaded)),
		UnresolvedCharges: nonNil(unresolvedCharges(loaded)),
		UnresolvedModules: nonNil(uniqueStrings(unresolved)),
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Fit stats: %s (%s)\n", card.Hull, card.Basis)
	writeDPS(&b, card)
	writeTank(&b, card.Tank)
	fmt.Fprintf(&b, "Speed %.0f m/s%s · align %.1fs · signature %.0f m\n", card.SpeedMS, propSuffix(card.PropOn), card.AlignS, card.SignatureM)
	writeCap(&b, stats)
	if withLoad {
		rep := fit.Validate(ctx, s, loaded, nil, false)
		card.Load = &FitLoad{
			CPU: ResourceUsage{Used: rep.CPUUsed, Capacity: rep.CPUCap, Unit: "tf", Over: overState(rep.CPUUsed, rep.CPUCap, "CPU", rep)},
			PG:  ResourceUsage{Used: rep.PGUsed, Capacity: rep.PGCap, Unit: "MW", Over: overState(rep.PGUsed, rep.PGCap, "PG", rep)},
		}
		fmt.Fprintf(&b, "CPU %.1f/%.1f tf%s · PG %.1f/%.1f MW%s\n",
			rep.CPUUsed, rep.CPUCap, overFlag(rep.CPUUsed, rep.CPUCap, "CPU", rep), rep.PGUsed, rep.PGCap, overFlag(rep.PGUsed, rep.PGCap, "PG", rep))
	}
	if len(card.NoAmmo) > 0 {
		fmt.Fprintf(&b, "No ammo loaded: %s (no default for this weapon family; counted as 0 DPS)\n", strings.Join(card.NoAmmo, ", "))
	}
	if len(card.UnresolvedCharges) > 0 {
		fmt.Fprintf(&b, "Unresolved charges (not loaded): %s\n", strings.Join(card.UnresolvedCharges, ", "))
	}
	if len(card.UnresolvedModules) > 0 {
		fmt.Fprintf(&b, "Unresolved modules (not counted): %s\n", strings.Join(card.UnresolvedModules, ", "))
	}
	b.WriteString("Basis: no implants or boosters; theoretical DPS (no reload); EHP at a uniform 25/25/25/25 damage profile.")
	return b.String(), card, ""
}

// weaponGroups folds the per-weapon DPS into one group per weapon+charge label, in
// first-seen order. defaultLabels marks the labels whose ammo the engine chose.
func weaponGroups(stats fit.FitStats, defaultLabels map[string]bool) []WeaponGroup {
	groups := []WeaponGroup{}
	index := map[string]int{}
	for _, w := range stats.DPS.PerWeapon {
		i, ok := index[w.Weapon]
		if !ok {
			i = len(groups)
			index[w.Weapon] = i
			groups = append(groups, WeaponGroup{Label: w.Weapon, DefaultAmmo: defaultLabels[w.Weapon]})
		}
		groups[i].Count++
		groups[i].DPS += w.Theoretical
	}
	return groups
}

// droneFlight is the drone stacks of the fit and what they do, or nil when the drones
// add no damage (under half a point of DPS).
func droneFlight(stats fit.FitStats, f fit.Fit) *DroneFlight {
	if stats.Drones.DPS < 0.5 {
		return nil
	}
	stacks := make([]DroneStack, 0, len(f.Drones))
	for _, d := range f.Drones {
		qty := d.Qty
		if qty < 1 {
			qty = 1
		}
		stacks = append(stacks, DroneStack{Name: d.Name, Qty: qty})
	}
	return &DroneFlight{Stacks: stacks, Launched: stats.Drones.Active, DPS: stats.Drones.DPS}
}

// tankCard is EHP per layer (uniform damage profile, as the engine computes it), the
// resists behind it and the active rep.
func tankCard(stats fit.FitStats) TankCard {
	t := stats.Tank
	resists := func(r fit.ResistProfile) Resists {
		return Resists{EM: r.EM, Thermal: r.Therm, Kinetic: r.Kin, Explosive: r.Exp}
	}
	return TankCard{
		EHP:             t.TotalEHP,
		ShieldEHP:       layerEHP(t.ShieldHP, t.ShieldResists),
		ArmorEHP:        layerEHP(t.ArmorHP, t.ArmorResists),
		HullEHP:         layerEHP(t.HullHP, t.HullResists),
		ActiveRepPerSec: t.ActiveRepPerSec,
		ShieldResists:   resists(t.ShieldResists),
		ArmorResists:    resists(t.ArmorResists),
		HullResists:     resists(t.HullResists),
	}
}

// capCard is the capacitor verdict the card prints: see writeCap.
func capCard(stats fit.FitStats) CapCard {
	return CapCard{
		CapacityGJ: stats.Capacitor.Capacity,
		Stable:     stats.Capacitor.Stable,
		Summary:    strings.TrimPrefix(gofa.CapSummary(stats), "cap "),
		ChargeFed:  gofa.ChargeFedCap(stats),
	}
}

// writeDPS prints the DPS headline, one line per weapon group with the charge it
// fires, and the drone flight.
func writeDPS(b *strings.Builder, c *FitStatCard) {
	fmt.Fprintf(b, "DPS %.0f", c.DPS)
	if c.WeaponDPS >= 0.5 && c.DroneDPS >= 0.5 {
		fmt.Fprintf(b, " (weapons %.0f, drones %.0f)", c.WeaponDPS, c.DroneDPS)
	}
	fmt.Fprintf(b, " · volley %.0f\n", c.Volley)

	for _, g := range c.Weapons {
		suffix := ""
		if g.DefaultAmmo {
			suffix = " (default ammo)"
		}
		fmt.Fprintf(b, "  %dx %s%s: %.0f DPS\n", g.Count, g.Label, suffix, g.DPS)
	}
	if d := c.Drones; d != nil {
		names := make([]string, len(d.Stacks))
		for i, st := range d.Stacks {
			names[i] = fmt.Sprintf("%s x%d", st.Name, st.Qty)
		}
		fmt.Fprintf(b, "  Drones %s (%d launched): %.0f DPS\n", strings.Join(names, ", "), d.Launched, d.DPS)
	}
}

// writeTank prints EHP per layer and the resists behind it.
func writeTank(b *strings.Builder, t TankCard) {
	fmt.Fprintf(b, "EHP %.0f (shield %.0f, armor %.0f, hull %.0f)", t.EHP, t.ShieldEHP, t.ArmorEHP, t.HullEHP)
	if t.ActiveRepPerSec >= 0.5 {
		fmt.Fprintf(b, " · active rep %.0f HP/s", t.ActiveRepPerSec)
	}
	b.WriteString("\n")
	pct := func(r Resists) string {
		return fmt.Sprintf("%.0f/%.0f/%.0f/%.0f", r.EM*100, r.Thermal*100, r.Kinetic*100, r.Explosive*100)
	}
	fmt.Fprintf(b, "Resists EM/Th/Kin/Exp %%: shield %s · armor %s · hull %s\n",
		pct(t.ShieldResists), pct(t.ArmorResists), pct(t.HullResists))
}

// overState grades a resource used past its cap, like fit.Validate: a Soft
// "<unit> over:" violation (at most 5 % over) closes with an EE-605 / EG-605
// implant; anything else past the cap cannot be flown.
func overState(used, capacity float64, unit string, rep fit.Report) OverState {
	if capacity <= 0 || used <= capacity {
		return OverNone
	}
	for _, v := range rep.Violations {
		if v.Severity == fit.Soft && strings.HasPrefix(v.Message, unit+" over:") {
			return OverSoft
		}
	}
	return OverHard
}

// overFlag marks a resource used past its cap in the card (see overState).
func overFlag(used, capacity float64, unit string, rep fit.Report) string {
	over := (used/capacity - 1) * 100
	switch overState(used, capacity, unit, rep) {
	case OverSoft:
		return fmt.Sprintf(" [+%.1f %%, fits with a 5 %% implant]", over)
	case OverHard:
		return fmt.Sprintf(" [OVER +%.1f %%]", over)
	}
	return ""
}

// layerEHP is HP over the mean resonance (1 - mean resist), the engine's formula.
func layerEHP(hp float64, r fit.ResistProfile) float64 {
	mean := (r.EM + r.Therm + r.Kin + r.Exp) / 4
	if mean >= 1 {
		return hp
	}
	return hp / (1 - mean)
}

// writeCap prints the capacitor verdict, or why there is none to give.
func writeCap(b *strings.Builder, stats fit.FitStats) {
	if text := gofa.CapSummary(stats); text != "" {
		fmt.Fprintf(b, "Cap: %s · %.0f GJ\n", strings.TrimPrefix(text, "cap "), stats.Capacitor.Capacity)
		return
	}
	if gofa.ChargeFedCap(stats) {
		b.WriteString("Cap: n/a - ancillary reps run on charges, not on capacitor\n")
	}
}

// propOn reports whether the fit has an online afterburner or MWD: the engine's max
// velocity already includes it.
func propOn(s *sde.SDE, f fit.Fit) bool {
	for _, m := range f.Mid {
		if m.State == "offline" {
			continue
		}
		if g := s.GetGroupID(m.TypeID); g != nil && *g == groupPropulsionModule {
			return true
		}
	}
	return false
}

// propSuffix is " (prop on)" when propOn.
func propSuffix(on bool) string {
	if on {
		return " (prop on)"
	}
	return ""
}

// unloadedWeapons names the online high-slot turrets / launchers that ended up
// with no charge and no charge was requested on the EFT line.
func unloadedWeapons(s *sde.SDE, f fit.Fit) []string {
	counts := map[string]int{}
	var order []string
	for _, m := range f.High {
		if m.ChargeID != 0 || m.Charge != "" || m.State == "offline" {
			continue
		}
		d := s.GetDogma(m.TypeID)
		if d[attrWeaponSpeed] <= 0 || d[attrChargeGroup1] == 0 {
			continue
		}
		if counts[m.Name] == 0 {
			order = append(order, m.Name)
		}
		n := m.Qty
		if n < 1 {
			n = 1
		}
		counts[m.Name] += n
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		if counts[name] > 1 {
			name = fmt.Sprintf("%s x%d", name, counts[name])
		}
		out = append(out, name)
	}
	return out
}

// unresolvedCharges lists charge names written on an EFT line that did not
// resolve (they are neither loaded nor replaced by a default).
func unresolvedCharges(f fit.Fit) []string {
	var out []string
	for _, m := range f.High {
		if m.Charge != "" && m.ChargeID == 0 {
			out = append(out, m.Charge)
		}
	}
	return uniqueStrings(out)
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// stripEFTFences drops markdown code-fence lines the model may wrap an EFT in.
func stripEFTFences(eft string) string {
	lines := strings.Split(eft, "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}
