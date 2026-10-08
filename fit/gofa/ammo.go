package gofa

import (
	"strings"

	"eve-cyno.dev/go/data/fit"
)

// Dogma attributes used to decide whether a charge fits a weapon module.
const (
	attrChargeSize = 128 // chargeSize: 1 = S, 2 = M, 3 = L, 4 = XL (on modules and charges)
)

// attrChargeGroups are chargeGroup1..chargeGroup5 (604, 605, 606, 609, 610): the
// charge groupIDs a module accepts.
var attrChargeGroups = []int{604, 605, 606, 609, 610}

// ammoSpec is one row of the default-ammo table. sized rows name a turret ammo
// family; the module's chargeSize (S/M/L/XL) completes the typeName.
type ammoSpec struct {
	name  string
	sized bool
}

// defaultAmmoTable maps a weapon module's groupID to the charge loaded when the
// EFT line carries none. It is deliberately small: one row per launcher / turret
// family, T1 standard ammo so the card is reproducible and Alpha-legal.
//
//	Rocket Launcher                                 507        Scourge Rocket
//	Light / Rapid Light Missile Launcher            509 / 511  Scourge Light Missile
//	Heavy Assault Missile Launcher                  771        Scourge Heavy Assault Missile
//	Heavy / Rapid Heavy Missile Launcher            510 / 1245 Scourge Heavy Missile
//	Cruise Missile Launcher                         506        Scourge Cruise Missile
//	Torpedo / Rapid Torpedo Launcher                508 / 1673 Scourge Torpedo
//	Projectile turrets (autocannon, artillery)      55         Phased Plasma S/M/L/XL
//	Hybrid turrets (blaster, railgun)               74         Antimatter Charge S/M/L/XL
//	Energy turrets (pulse, beam laser)              53         Multifrequency S/M/L/XL
//
// Missiles default to Scourge (kinetic) and turrets to the highest-damage T1
// ammo of their family (damage is identical across the T1 types of one tier).
// Weapon families not listed (Triglavian / Vorton, XL launchers, bombs) stay
// unloaded and therefore report no DPS.
var defaultAmmoTable = map[int]ammoSpec{
	507:  {name: "Scourge Rocket"},
	509:  {name: "Scourge Light Missile"},
	511:  {name: "Scourge Light Missile"},
	771:  {name: "Scourge Heavy Assault Missile"},
	510:  {name: "Scourge Heavy Missile"},
	1245: {name: "Scourge Heavy Missile"},
	506:  {name: "Scourge Cruise Missile"},
	508:  {name: "Scourge Torpedo"},
	1673: {name: "Scourge Torpedo"},
	55:   {name: "Phased Plasma", sized: true},
	74:   {name: "Antimatter Charge", sized: true},
	53:   {name: "Multifrequency", sized: true},
}

// ammoSizeSuffix maps a module's chargeSize attribute to the ammo size suffix.
var ammoSizeSuffix = map[int]string{1: "S", 2: "M", 3: "L", 4: "XL"}

// ammoLookup is the extra SDE surface ApplyDefaultAmmo needs on top of SDE: name
// resolution for the charge. *sde.SDE satisfies it; stubs that do not simply get
// no default ammo.
type ammoLookup interface {
	ResolveNames(names []string) map[string]int
	GetTypeName(typeID int) *string
}

// AmmoUse reports the charge ApplyDefaultAmmo loaded into one weapon group.
type AmmoUse struct {
	Module  string // weapon module name, e.g. "Rocket Launcher II"
	Charge  string // charge loaded into every Module instance that had none
	Count   int    // number of module instances loaded
	Default bool   // true: the table default; false: a charge the caller asked for
}

// WeaponLabel is the PerWeapon label the engine gives a weapon: module and the
// charge it fires.
func WeaponLabel(module, charge string) string {
	return module + " [" + charge + "]"
}

// ApplyDefaultAmmo returns a copy of f in which every high-slot weapon that has
// no charge on its EFT line is loaded with a charge, plus the list of charges
// it loaded (one entry per module + charge pair, in fit order). The input fit is
// not modified.
//
// requested maps a module name (case-insensitive) to a charge name and wins over
// the table default when the charge is compatible with the module (right charge
// group and size); an incompatible or unknown request falls back to the default.
// A module whose EFT line already names a charge — even one that did not resolve
// — is left alone, as are offline modules and modules outside defaultAmmoTable.
func (e *Engine) ApplyDefaultAmmo(f fit.Fit, requested map[string]string) (fit.Fit, []AmmoUse) {
	nr, ok := e.sde.(ammoLookup)
	if !ok {
		return f, nil
	}
	want := make(map[string]string, len(requested))
	for module, charge := range requested {
		want[strings.ToLower(strings.TrimSpace(module))] = strings.TrimSpace(charge)
	}

	type pick struct {
		charge   string
		chargeID int
		isDef    bool
		ok       bool
	}
	cache := map[int]pick{}
	choose := func(m fit.FitModule) pick {
		if p, hit := cache[m.TypeID]; hit {
			return p
		}
		p := pick{}
		attrs := e.sde.GetDogma(m.TypeID)
		try := func(name string) (string, int, bool) {
			id := nr.ResolveNames([]string{name})[name]
			if id == 0 || !e.chargeFits(attrs, id) {
				return "", 0, false
			}
			canon := name
			if n := nr.GetTypeName(id); n != nil {
				canon = *n
			}
			return canon, id, true
		}
		if name, ok := want[strings.ToLower(m.Name)]; ok && name != "" {
			if c, id, fits := try(name); fits {
				p = pick{charge: c, chargeID: id, ok: true}
			}
		}
		if !p.ok {
			if gid := e.sde.GetGroupID(m.TypeID); gid != nil {
				if spec, listed := defaultAmmoTable[*gid]; listed {
					name := spec.name
					if spec.sized {
						name += " " + ammoSizeSuffix[int(attrs[attrChargeSize])]
					}
					if c, id, fits := try(name); fits {
						p = pick{charge: c, chargeID: id, isDef: true, ok: true}
					}
				}
			}
		}
		cache[m.TypeID] = p
		return p
	}

	out := f
	out.High = append([]fit.FitModule(nil), f.High...)
	var uses []AmmoUse
	index := map[string]int{} // module + "\x00" + charge → position in uses
	for i := range out.High {
		m := &out.High[i]
		if m.ChargeID != 0 || m.Charge != "" || m.State == "offline" {
			continue
		}
		p := choose(*m)
		if !p.ok {
			continue
		}
		m.Charge, m.ChargeID = p.charge, p.chargeID
		n := m.Qty
		if n <= 0 {
			n = 1
		}
		key := m.Name + "\x00" + p.charge
		if at, seen := index[key]; seen {
			uses[at].Count += n
			continue
		}
		index[key] = len(uses)
		uses = append(uses, AmmoUse{Module: m.Name, Charge: p.charge, Count: n, Default: p.isDef})
	}
	return out, uses
}

// chargeFits reports whether the charge typeID can be loaded into a module with
// the given dogma attributes: its groupID is one of the module's chargeGroups
// and, when both declare one, the chargeSize matches.
func (e *Engine) chargeFits(moduleAttrs map[int]float64, chargeID int) bool {
	gid := e.sde.GetGroupID(chargeID)
	if gid == nil {
		return false
	}
	accepted := false
	for _, a := range attrChargeGroups {
		if v := moduleAttrs[a]; v != 0 && int(v) == *gid {
			accepted = true
			break
		}
	}
	if !accepted {
		return false
	}
	ms := moduleAttrs[attrChargeSize]
	cs := e.sde.GetDogma(chargeID)[attrChargeSize]
	return ms == 0 || cs == 0 || ms == cs
}

// WithDefaultAmmo returns an Engine whose Stats loads default ammo (see
// ApplyDefaultAmmo) into weapons that have none and flags the result Estimated.
// The receiver is not modified; use it for chat surfaces, where an EFT without
// ammo should still report a DPS. The fitting window keeps the plain Engine so
// an intentionally unloaded weapon stays unloaded.
func (e *Engine) WithDefaultAmmo() *Engine {
	c := *e
	c.defaultAmmo = true
	return &c
}
