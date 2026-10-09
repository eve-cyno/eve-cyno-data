// Command abyssgen derives core/abyss/abyss_data.json from the SDE SQLite file: the
// Abyssal Deadspace NPCs, weather environments, filament tiers and hazard clouds. Only SDE
// tables are read (invTypes, invGroups, dgmTypeAttributes, dgmTypeEffects); nothing is
// fetched and no wiki text is used. It runs through `go generate ./abyss` in the core
// module (the directive is in core/abyss/generate.go).
//
//	ABYSS_SDE_BUILD=3586130_20261007 go run ./internal/abyssgen -out abyss/abyss_data.json -doc ../data/knowledge/eve_abyss_weather_and_npcs.md
//
// -doc also renders the RAG knowledge document from the same dataset (monorepo only).
//
// The SDE path is -sde, else EVE_CORE_SDE_PATH, else data/sde/sde.sqlite. The build id is
// -build, else ABYSS_SDE_BUILD, else the first line of "<sde>.build" (written by
// core/cmd/sde); it is required so the file always says which SDE it came from. Output is
// deterministic: sorted, rounded, no timestamps.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/abyss"
	"eve-cyno.dev/go/data/config"
	_ "modernc.org/sqlite"
)

// SDE groups that hold the abyssal NPCs, the weather environments, the filaments and the
// hazard clouds.
const (
	groupSpaceshipEntities = 1982
	groupDroneEntities     = 1997
	groupEnvironment       = 1983
	groupFilaments         = 1979
	groupHazards           = 1971
)

// Dogma attribute IDs read here (names are the SDE attributeName).
const (
	attrHP             = 9   // hp (hull)
	attrSpeedFactor    = 20  // speedFactor (web strength, percent)
	attrMaxVelocity    = 37  // maxVelocity
	attrCycle          = 51  // speed (turret cycle, ms)
	attrMaxRange       = 54  // maxRange (optimal)
	attrDamageMult     = 64  // damageMultiplier
	attrShieldBonus    = 68  // shieldBonus (shield rep amount)
	attrArmorRepAmount = 84  // armorDamageAmount (armor rep amount)
	attrNeutAmount     = 97  // energyNeutralizerAmount
	attrKineticRes     = 109 // hull resonances
	attrThermalRes     = 110
	attrExplosiveRes   = 111
	attrEMRes          = 113
	attrEMDmg          = 114
	attrExplosiveDmg   = 116
	attrKineticDmg     = 117
	attrThermalDmg     = 118
	attrFalloff        = 158
	attrTracking       = 160
	attrMissileMult    = 212
	attrShieldCap      = 263
	attrArmorHP        = 265
	attrArmorEMRes     = 267
	attrArmorExpRes    = 268
	attrArmorKinRes    = 269
	attrArmorThermRes  = 270
	attrShieldEMRes    = 271
	attrShieldExpRes   = 272
	attrShieldKinRes   = 273
	attrShieldThermRes = 274
	attrDampStrength   = 309 // maxTargetRangeBonus (percent)
	attrMissileLaunch  = 506
	attrMissileType    = 507
	attrSignature      = 552
	attrPainterBonus   = 554 // signatureRadiusBonus (percent)
	attrMissileVelMult = 645
	attrMissileTimeMul = 646
	attrExplosionDelay = 281
	attrGuidanceVel    = 847 // aoeVelocityBonus (percent)
	attrWeatherID      = 2760
	attrDifficultyTier = 2761
	attrRampPerCycle   = 2733
	attrRampMax        = 2734
	attrOrbitRange     = 2786
	attrBuff1ID        = 2468
	attrBuff1Val       = 2469
	attrBuff2ID        = 2470
	attrBuff2Val       = 2471
)

// Dogma effect IDs read here.
const (
	effTargetAttack  = 10
	effMissile       = 569
	effDisintegrator = 6995
	effChain         = 8088
)

// behavior describes how one NPC ewar / support effect is read: the duration, range and
// falloff attribute IDs and where the strength comes from (0 = none).
type behavior struct {
	kind                   string
	effect                 int
	duration, rng, falloff int
	strength               int
	unit                   string
	strengthFallback       int // used when strength is absent or zero (local reps)
	perSecond              bool
	negateStrength         bool
}

var behaviors = []behavior{
	{kind: abyss.EffWeb, effect: 6743, duration: 2499, rng: 2500, falloff: 2501, strength: attrSpeedFactor, unit: "percent speed reduction", negateStrength: true},
	{kind: abyss.EffWarpScramble, effect: 6745, duration: 2506, rng: 2507, strength: 2509, unit: "warp disruption points"},
	{kind: abyss.EffNeutralizer, effect: 6756, duration: 2519, rng: 2520, falloff: 2521, strength: attrNeutAmount, unit: "GJ per cycle", perSecond: true},
	{kind: abyss.EffTargetPainter, effect: 6754, duration: 2523, rng: 2524, falloff: 2525, strength: attrPainterBonus, unit: "percent signature radius"},
	{kind: abyss.EffSensorDamp, effect: 6755, duration: 2527, rng: 2528, falloff: 2529, strength: attrDampStrength, unit: "percent targeting range"},
	{kind: abyss.EffGuidanceDisruptor, effect: 6746, duration: 2511, rng: 2512, falloff: 2513, strength: attrGuidanceVel, unit: "percent explosion velocity"},
	{kind: abyss.EffTrackingDisruptor, effect: 6747, duration: 2515, rng: 2516, falloff: 2517},
	{kind: abyss.EffRemoteArmorRepair, effect: 6741, duration: 2491, rng: 2492, falloff: 2493, strength: attrArmorRepAmount, unit: "armor HP per cycle", perSecond: true},
	{kind: abyss.EffRemoteShieldBoost, effect: 6742, duration: 2495, rng: 2496, falloff: 2497, strength: attrShieldBonus, unit: "shield HP per cycle", perSecond: true},
	{kind: abyss.EffLocalArmorRepair, effect: 6884, duration: 2633, strength: 2635, strengthFallback: attrArmorRepAmount, unit: "armor HP per cycle", perSecond: true},
	{kind: abyss.EffLocalShieldBoost, effect: 6990, duration: 2725, strength: 2723, strengthFallback: attrShieldBonus, unit: "shield HP per cycle", perSecond: true},
	{kind: abyss.EffChainLightning, effect: effChain},
}

var filamentName = regexp.MustCompile(`^([A-Z][a-z]+) ([A-Z][a-z]+) Filament$`)

func main() {
	out := flag.String("out", "abyss_data.json", "output JSON path")
	sdePath := flag.String("sde", "", "SDE SQLite path (default EVE_CORE_SDE_PATH / data/sde/sde.sqlite)")
	build := flag.String("build", "", "SDE build id (default ABYSS_SDE_BUILD, else <sde>.build)")
	docOut := flag.String("doc", "", "also write the RAG knowledge document (Markdown) to this path")
	flag.Parse()
	if err := run(*sdePath, *build, *out, *docOut); err != nil {
		fmt.Fprintln(os.Stderr, "abyssgen:", err)
		os.Exit(1)
	}
}

func run(sdePath, build, out, docOut string) error {
	if sdePath == "" {
		sdePath = config.Load().SDEPath
	}
	if build == "" {
		build = os.Getenv("ABYSS_SDE_BUILD")
	}
	if build == "" {
		if b, err := os.ReadFile(sdePath + ".build"); err == nil {
			build = strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
		}
	}
	if build == "" {
		return fmt.Errorf("no SDE build id: pass -build, set ABYSS_SDE_BUILD or provide %s.build", sdePath)
	}
	db, err := sql.Open("sqlite", "file:"+sdePath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open %s: %w", sdePath, err)
	}
	defer db.Close()
	ds, err := Build(db, build)
	if err != nil {
		return err
	}
	b, err := Marshal(ds)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(out), err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	if docOut != "" {
		if err := os.MkdirAll(filepath.Dir(docOut), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(docOut), err)
		}
		if err := os.WriteFile(docOut, []byte(RenderDoc(ds)), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", docOut, err)
		}
	}
	fmt.Printf("abyssgen: %s: %d NPCs, %d weather, %d tiers, %d hazards (SDE %s)\n",
		out, len(ds.NPCs), len(ds.Weather), len(ds.Tiers), len(ds.Hazards), build)
	return nil
}

// Marshal renders the dataset as indented JSON with a trailing newline, without HTML escaping.
func Marshal(ds *abyss.Dataset) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(ds); err != nil {
		return nil, fmt.Errorf("encode dataset: %w", err)
	}
	return buf.Bytes(), nil
}

type attrs map[int]float64

type typeRow struct {
	id          int
	name, group string
}

// Build derives the dataset from an open SDE database.
func Build(db *sql.DB, build string) (*abyss.Dataset, error) {
	ds := &abyss.Dataset{
		SchemaVersion: abyss.SchemaVersion,
		Source:        "EVE SDE (Fenris Creations)",
		SDEBuild:      build,
		Notes: []string{
			"Derived only from SDE tables invTypes, invGroups, dgmTypeAttributes and dgmTypeEffects.",
			"The SDE gives the weather environments no dogma: per-weather, per-tier penalties and bonuses are not in it.",
			"The SDE does not say which NPCs spawn in which tier or weather.",
			"Damage and EHP are derived (see package doc); kill priority is a separate heuristic of ours and is not stored here.",
		},
	}
	npcRows, err := typesInGroups(db, groupSpaceshipEntities, groupDroneEntities)
	if err != nil {
		return nil, err
	}
	npcAttrs, err := attrsOfGroups(db, groupSpaceshipEntities, groupDroneEntities)
	if err != nil {
		return nil, err
	}
	npcEffects, err := effectsOfGroups(db, groupSpaceshipEntities, groupDroneEntities)
	if err != nil {
		return nil, err
	}
	missileIDs := map[int]bool{}
	for _, a := range npcAttrs {
		if v := a[attrMissileType]; v > 0 {
			missileIDs[int(v)] = true
		}
	}
	charges, err := attrsOfTypes(db, missileIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range npcRows {
		if strings.Contains(strings.ToLower(r.name), "placeholder") {
			continue
		}
		ds.NPCs = append(ds.NPCs, buildNPC(r, npcAttrs[r.id], npcEffects[r.id], charges))
	}
	sort.Slice(ds.NPCs, func(i, j int) bool {
		if ds.NPCs[i].Name != ds.NPCs[j].Name {
			return ds.NPCs[i].Name < ds.NPCs[j].Name
		}
		return ds.NPCs[i].TypeID < ds.NPCs[j].TypeID
	})
	if err := buildWeather(db, ds); err != nil {
		return nil, err
	}
	if err := buildHazards(db, ds); err != nil {
		return nil, err
	}
	return ds, nil
}

func buildWeather(db *sql.DB, ds *abyss.Dataset) error {
	fils, err := typesInGroups(db, groupFilaments)
	if err != nil {
		return err
	}
	fattrs, err := attrsOfGroups(db, groupFilaments)
	if err != nil {
		return err
	}
	envs, err := typesInGroups(db, groupEnvironment)
	if err != nil {
		return err
	}
	envByID := map[int]typeRow{}
	for _, e := range envs {
		envByID[e.id] = e
	}
	tiers := map[int]*abyss.Tier{}
	weather := map[int]*abyss.Weather{}
	for _, f := range fils {
		m := filamentName.FindStringSubmatch(f.name)
		a := fattrs[f.id]
		tierV, hasTier := a[attrDifficultyTier]
		wID, hasW := a[attrWeatherID]
		if m == nil || !hasTier || !hasW || tierV < 0 || tierV > 6 {
			continue // demo / placeholder filaments
		}
		ti, wi := int(tierV), int(wID)
		env, ok := envByID[wi]
		if !ok {
			return fmt.Errorf("filament %q references unknown weather type %d", f.name, wi)
		}
		t := tiers[ti]
		if t == nil {
			t = &abyss.Tier{Index: ti, Name: m[1]}
			tiers[ti] = t
		}
		if t.Name != m[1] {
			return fmt.Errorf("tier %d has two names: %q and %q", ti, t.Name, m[1])
		}
		w := weather[wi]
		if w == nil {
			w = &abyss.Weather{TypeID: wi, Name: m[2], SDEName: env.name}
			weather[wi] = w
		}
		if w.Name != m[2] {
			return fmt.Errorf("weather %d has two names: %q and %q", wi, w.Name, m[2])
		}
		t.Filaments = append(t.Filaments, abyss.FilamentRef{TypeID: f.id, Name: f.name, Weather: m[2]})
		w.FilamentTypeID = append(w.FilamentTypeID, f.id)
	}
	for _, t := range tiers {
		sort.Slice(t.Filaments, func(i, j int) bool { return t.Filaments[i].Weather < t.Filaments[j].Weather })
		ds.Tiers = append(ds.Tiers, *t)
	}
	sort.Slice(ds.Tiers, func(i, j int) bool { return ds.Tiers[i].Index < ds.Tiers[j].Index })
	tierOf := map[int]int{}
	for _, f := range fils {
		tierOf[f.id] = int(fattrs[f.id][attrDifficultyTier])
	}
	for _, w := range weather {
		sort.Slice(w.FilamentTypeID, func(i, j int) bool {
			a, b := w.FilamentTypeID[i], w.FilamentTypeID[j]
			if tierOf[a] != tierOf[b] {
				return tierOf[a] < tierOf[b]
			}
			return a < b
		})
		ds.Weather = append(ds.Weather, *w)
	}
	sort.Slice(ds.Weather, func(i, j int) bool { return ds.Weather[i].Name < ds.Weather[j].Name })
	if len(ds.Tiers) == 0 || len(ds.Weather) == 0 {
		return fmt.Errorf("no abyssal filaments found in the SDE (group %d)", groupFilaments)
	}
	return nil
}

func buildHazards(db *sql.DB, ds *abyss.Dataset) error {
	rows, err := typesInGroups(db, groupHazards)
	if err != nil {
		return err
	}
	at, err := attrsOfGroups(db, groupHazards)
	if err != nil {
		return err
	}
	for _, r := range rows {
		a := at[r.id]
		low := strings.ToLower(r.name)
		if len(a) == 0 || strings.Contains(low, "proving") || strings.Contains(low, "do not translate") ||
			strings.Contains(low, "union day") || strings.Contains(low, "demo") {
			continue
		}
		h := abyss.Hazard{TypeID: r.id, Name: r.name, RangeM: round(a[attrMaxRange])}
		if id, ok := a[attrBuff1ID]; ok {
			h.Buffs = append(h.Buffs, abyss.RawBuff{ID: int(id), Value: round(a[attrBuff1Val])})
		}
		if id, ok := a[attrBuff2ID]; ok {
			h.Buffs = append(h.Buffs, abyss.RawBuff{ID: int(id), Value: round(a[attrBuff2Val])})
		}
		if h.RangeM == 0 && len(h.Buffs) == 0 {
			continue // carries only flags (untargetable, ...), nothing to report
		}
		ds.Hazards = append(ds.Hazards, h)
	}
	sort.Slice(ds.Hazards, func(i, j int) bool { return ds.Hazards[i].TypeID < ds.Hazards[j].TypeID })
	return nil
}

func buildNPC(r typeRow, a attrs, effs map[int]bool, charges map[int]attrs) abyss.NPC {
	res := func(ids ...int) abyss.Resists {
		pct := func(id int) float64 { return round((1 - a[id]) * 100) }
		return abyss.Resists{EM: pct(ids[0]), Thermal: pct(ids[1]), Kinetic: pct(ids[2]), Explosive: pct(ids[3])}
	}
	n := abyss.NPC{
		TypeID: r.id, Name: r.name, Group: r.group, Family: family(r.name),
		Shield:          abyss.Layer{HP: round(a[attrShieldCap]), Resists: res(attrShieldEMRes, attrShieldThermRes, attrShieldKinRes, attrShieldExpRes)},
		Armor:           abyss.Layer{HP: round(a[attrArmorHP]), Resists: res(attrArmorEMRes, attrArmorThermRes, attrArmorKinRes, attrArmorExpRes)},
		Hull:            abyss.Layer{HP: round(a[attrHP]), Resists: res(attrEMRes, attrThermalRes, attrKineticRes, attrExplosiveRes)},
		MaxVelocity:     round(a[attrMaxVelocity]),
		SignatureRadius: round(a[attrSignature]),
		OrbitRangeM:     round(a[attrOrbitRange]),
	}
	n.TotalHP = round(n.Shield.HP + n.Armor.HP + n.Hull.HP)
	n.EHP = round(layerEHP(a, attrShieldCap, attrShieldEMRes, attrShieldThermRes, attrShieldKinRes, attrShieldExpRes) +
		layerEHP(a, attrArmorHP, attrArmorEMRes, attrArmorThermRes, attrArmorKinRes, attrArmorExpRes) +
		layerEHP(a, attrHP, attrEMRes, attrThermalRes, attrKineticRes, attrExplosiveRes))
	n.Damage = buildDamage(a, effs, charges)
	for _, b := range behaviors {
		if !effs[b.effect] {
			continue
		}
		e := abyss.Effect{Kind: b.kind, StrengthUnit: b.unit}
		e.DurationS = round(a[b.duration] / 1000)
		if b.rng != 0 {
			e.RangeM = round(a[b.rng])
		}
		if b.falloff != 0 {
			e.FalloffM = round(a[b.falloff])
		}
		if b.strength != 0 {
			s := a[b.strength]
			if s == 0 && b.strengthFallback != 0 {
				s = a[b.strengthFallback]
			}
			if b.negateStrength {
				s = -s
			}
			e.Strength = round(s)
		}
		if b.kind == abyss.EffTrackingDisruptor {
			e.Strength = 0
		}
		if b.perSecond && e.DurationS > 0 {
			e.PerSecond = round(e.Strength / e.DurationS)
		}
		n.Effects = append(n.Effects, e)
	}
	return n
}

func layerEHP(a attrs, hp int, res ...int) float64 {
	var sum float64
	for _, id := range res {
		sum += a[id]
	}
	mean := sum / float64(len(res))
	if a[hp] <= 0 || mean <= 0 {
		return a[hp]
	}
	return a[hp] / mean
}

func buildDamage(a attrs, effs map[int]bool, charges map[int]attrs) abyss.Damage {
	d := turretDamage(a, effs)
	if d.Volley == 0 && effs[effMissile] {
		if m := missileDamage(a, charges); m.Volley > 0 {
			return m
		}
	}
	if d.Volley == 0 {
		return abyss.Damage{Kind: abyss.DamageNone} // a weapon effect without damage attributes (support hulls)
	}
	return d
}

// weaponDamage fills the damage components, volley, cycle, DPS and dominant type from
// the source attributes (the NPC for guns, the missile charge for missiles).
func weaponDamage(d abyss.Damage, src attrs, mult, cycleMS float64) abyss.Damage {
	d.EM = round(src[attrEMDmg] * mult)
	d.Thermal = round(src[attrThermalDmg] * mult)
	d.Kinetic = round(src[attrKineticDmg] * mult)
	d.Explosive = round(src[attrExplosiveDmg] * mult)
	d.Volley = round(d.EM + d.Thermal + d.Kinetic + d.Explosive)
	d.CycleS = round(cycleMS / 1000)
	if d.CycleS > 0 {
		d.DPS = round(d.Volley / d.CycleS)
	}
	best := 0.0
	for _, c := range []struct {
		name string
		v    float64
	}{{"em", d.EM}, {"thermal", d.Thermal}, {"kinetic", d.Kinetic}, {"explosive", d.Explosive}} {
		if c.v > best {
			best, d.Dominant = c.v, c.name
		}
	}
	return d
}

func turretDamage(a attrs, effs map[int]bool) abyss.Damage {
	d := abyss.Damage{Kind: abyss.DamageNone}
	switch {
	case effs[effDisintegrator]:
		d.Kind = abyss.DamageDisintegrator
	case effs[effTargetAttack]:
		d.Kind = abyss.DamageTurret
	default:
		return d
	}
	d.OptimalM = round(a[attrMaxRange])
	d.FalloffM = round(a[attrFalloff])
	d.TrackingSpeed = round(a[attrTracking])
	d = weaponDamage(d, a, a[attrDamageMult], a[attrCycle])
	if d.Kind == abyss.DamageDisintegrator {
		d.RampPerCycle = round(a[attrRampPerCycle])
		d.RampMax = round(a[attrRampMax])
	}
	return d
}

// missileDamage uses the NPC's missile charge (attribute 507): its four damage attributes
// times the NPC's missileDamageMultiplier (212), one launch per missileLaunchDuration (506).
// The range is charge velocity x charge flight time x the NPC's two multipliers (645, 646).
func missileDamage(a attrs, charges map[int]attrs) abyss.Damage {
	src := charges[int(a[attrMissileType])]
	d := abyss.Damage{Kind: abyss.DamageMissile}
	d.OptimalM = round(src[attrMaxVelocity] * a[attrMissileVelMult] * src[attrExplosionDelay] / 1000 * a[attrMissileTimeMul])
	return weaponDamage(d, src, a[attrMissileMult], a[attrMissileLaunch])
}

// familyPatterns maps a substring of the type name to the family label. The first match
// wins, so longer names come first. The label is only the hull/drone word(s) of the name.
var familyPatterns = []struct{ contains, family string }{
	{"Vila Damavik", "Vila Damavik"}, {"Vila Vedmak", "Vila Vedmak"},
	{"Damavik", "Damavik"}, {"Vedmak", "Vedmak"}, {"Kikimora", "Kikimora"}, {"Drekavac", "Drekavac"},
	{"Leshak", "Leshak"}, {"Rodiva", "Rodiva"}, {"Tessella", "Tessella"}, {"Tessera", "Tessera"},
	{"Swarmer", "Swarmer"}, {"Disparu Troop", "Disparu Troop"}, {"Abyssal Overmind", "Abyssal Overmind"},
	{"Drifter", "Drifter"}, {"Lucifer", "Lucifer"}, {"Ephialtes", "Ephialtes"},
	{"Lucid ", "Lucid"}, {"Devoted ", "Devoted"}, {"Awoken ", "Awoken"}, {"Tyrannos", "Tyrannos"},
}

func family(name string) string {
	for _, p := range familyPatterns {
		if strings.Contains(name, p.contains) {
			return p.family
		}
	}
	return "Other"
}

func round(v float64) float64 { return math.Round(v*10000) / 10000 }

func typesInGroups(db *sql.DB, groups ...int) ([]typeRow, error) {
	rows, err := db.Query(`SELECT t.typeID, t.typeName, g.groupName
		FROM invTypes t JOIN invGroups g ON g.groupID = t.groupID
		WHERE t.groupID IN (`+placeholders(len(groups))+`) ORDER BY t.typeID`, intArgs(groups)...)
	if err != nil {
		return nil, fmt.Errorf("query types: %w", err)
	}
	defer rows.Close()
	var out []typeRow
	for rows.Next() {
		var r typeRow
		if err := rows.Scan(&r.id, &r.name, &r.group); err != nil {
			return nil, fmt.Errorf("scan type: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func attrsOfGroups(db *sql.DB, groups ...int) (map[int]attrs, error) {
	return scanAttrs(db, `SELECT a.typeID, a.attributeID, COALESCE(a.valueFloat, a.valueInt)
		FROM dgmTypeAttributes a JOIN invTypes t ON t.typeID = a.typeID
		WHERE t.groupID IN (`+placeholders(len(groups))+`)`, intArgs(groups)...)
}

func attrsOfTypes(db *sql.DB, ids map[int]bool) (map[int]attrs, error) {
	if len(ids) == 0 {
		return map[int]attrs{}, nil
	}
	list := make([]int, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Ints(list)
	return scanAttrs(db, `SELECT typeID, attributeID, COALESCE(valueFloat, valueInt)
		FROM dgmTypeAttributes WHERE typeID IN (`+placeholders(len(list))+`)`, intArgs(list)...)
}

func scanAttrs(db *sql.DB, q string, args ...any) (map[int]attrs, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query attributes: %w", err)
	}
	defer rows.Close()
	out := map[int]attrs{}
	for rows.Next() {
		var t, a int
		var v sql.NullFloat64
		if err := rows.Scan(&t, &a, &v); err != nil {
			return nil, fmt.Errorf("scan attribute: %w", err)
		}
		if out[t] == nil {
			out[t] = attrs{}
		}
		out[t][a] = v.Float64
	}
	return out, rows.Err()
}

func effectsOfGroups(db *sql.DB, groups ...int) (map[int]map[int]bool, error) {
	rows, err := db.Query(`SELECT e.typeID, e.effectID FROM dgmTypeEffects e JOIN invTypes t ON t.typeID = e.typeID
		WHERE t.groupID IN (`+placeholders(len(groups))+`)`, intArgs(groups)...)
	if err != nil {
		return nil, fmt.Errorf("query effects: %w", err)
	}
	defer rows.Close()
	out := map[int]map[int]bool{}
	for rows.Next() {
		var t, e int
		if err := rows.Scan(&t, &e); err != nil {
			return nil, fmt.Errorf("scan effect: %w", err)
		}
		if out[t] == nil {
			out[t] = map[int]bool{}
		}
		out[t][e] = true
	}
	return out, rows.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func intArgs(v []int) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = x
	}
	return out
}
