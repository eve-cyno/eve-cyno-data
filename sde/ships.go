package sde

import (
	"fmt"
	"regexp"
	"strings"
)

// _shipGroupToClass mirrors Python _SHIP_GROUP_TO_CLASS verbatim.
var _shipGroupToClass = map[int]string{
	// Frigates
	25:   "frigate",
	324:  "frigate",
	830:  "frigate",
	831:  "frigate",
	834:  "frigate",
	893:  "frigate",
	1527: "frigate",
	1283: "frigate",
	// Destroyers
	420:  "destroyer",
	541:  "destroyer",
	1305: "destroyer",
	1534: "destroyer",
	// Cruisers
	26:  "cruiser",
	358: "cruiser",
	832: "cruiser",
	833: "cruiser",
	894: "cruiser",
	906: "cruiser",
	963: "cruiser",
	// Battlecruisers
	419:  "battlecruiser",
	1201: "battlecruiser",
	540:  "battlecruiser",
	// Battleships
	27:  "battleship",
	900: "battleship",
	898: "battleship",
}

// _shipClassSizePrefix mirrors Python _SHIP_CLASS_SIZE_PREFIX verbatim.
var _shipClassSizePrefix = map[string]string{
	"frigate":       "Small",
	"destroyer":     "Small",
	"cruiser":       "Medium",
	"battlecruiser": "Medium",
	"battleship":    "Large",
}

// bonusEntry mirrors one entry of _BONUS_TEXT_TO_FAMILY.
type bonusEntry struct {
	keyword string
	family  string
	sized   bool
}

// _bonusTextToFamily mirrors Python _BONUS_TEXT_TO_FAMILY verbatim.
var _bonusTextToFamily = []bonusEntry{
	{"entropic disintegrator damage", "Entropic Radiation Sink", false},
	{"entropic disintegrator rate of fire", "Entropic Radiation Sink", false},
	{"entropic disintegrator optimal", "Entropic Radiation Sink", false},
	{"entropic disintegrator tracking", "Tracking Enhancer", false},
	{"hybrid turret damage", "Magnetic Field Stabilizer", false},
	{"hybrid turret rate of fire", "Magnetic Field Stabilizer", false},
	{"hybrid turret tracking", "Tracking Enhancer", false},
	{"hybrid turret falloff", "Tracking Computer", false},
	{"hybrid turret optimal", "Tracking Computer", false},
	{"energy turret damage", "Heat Sink", false},
	{"energy turret rate of fire", "Heat Sink", false},
	{"energy turret tracking", "Tracking Enhancer", false},
	{"projectile turret damage", "Gyrostabilizer", false},
	{"projectile turret rate of fire", "Gyrostabilizer", false},
	{"projectile turret tracking", "Tracking Enhancer", false},
	{"missile damage", "Ballistic Control System", false},
	{"missile rate of fire", "Ballistic Control System", false},
	{"missile velocity", "Missile Guidance Computer", false},
	{"missile precision", "Missile Guidance Computer", false},
	{"drone damage", "Drone Damage Amplifier", false},
	{"drone hitpoints", "Drone Damage Amplifier", false},
	{"armor plate hitpoints", "Steel Plates", true},
	{"armor resistances", "Energized Adaptive Nano Membrane", true},
	{"armor repairer", "Armor Repairer", true},
	{"shield extender hitpoints", "Shield Extender", true},
	{"shield resistances", "Multispectrum Shield Hardener", true},
	{"shield boost", "Shield Booster", true},
	{"shield recharge", "Shield Power Relay", false},
	{"stasis webifier", "Stasis Webifier", false},
	{"warp disruptor", "Warp Disruptor", false},
	{"warp scrambler", "Warp Scrambler", false},
	{"energy neutralizer", "Energy Neutralizer", true},
	{"smart bomb", "Smart Bomb", true},
	{"microwarpdrive", "Microwarpdrive", true},
	{"afterburner", "Afterburner", true},
	{"remote armor repairer", "Remote Armor Repairer", true},
	{"remote capacitor", "Remote Capacitor Transmitter", true},
	{"capacitor injector", "Capacitor Booster", true},
	{"sensor strength", "Signal Amplifier", false},
	{"targeting range", "Sensor Booster", false},
	{"scan resolution", "Sensor Booster", false},
}

var (
	_reAnchorTag = regexp.MustCompile(`<a[^>]*>([^<]*)</a>`)
	_reHTMLTag   = regexp.MustCompile(`<[^>]+>`)
)

// GetShipClass maps a ship typeID to a coarse class name.
func (s *SDE) GetShipClass(typeID int) *string {
	gid := s.GetGroupID(typeID)
	if gid == nil {
		return nil
	}
	cls, ok := _shipGroupToClass[*gid]
	if !ok {
		return nil
	}
	return &cls
}

// GetShipTraits returns role/skill bonuses from invTraits for a typeID.
func (s *SDE) GetShipTraits(typeID int) []map[string]any {
	if !s.HasTable("invTraits") {
		return []map[string]any{}
	}
	rows, err := s.db.Query(`
		SELECT t.skillID, t.bonus, t.bonusText, t.unitID, inv.typeName AS skillName
		FROM invTraits t
		LEFT JOIN invTypes inv
			ON CAST(inv.typeID AS INTEGER) = CAST(t.skillID AS INTEGER)
		WHERE CAST(t.typeID AS INTEGER) = ?
		ORDER BY CAST(t.traitID AS INTEGER)`, typeID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var skillIDStr, bonus, bonusText, unitIDStr, skillName *string
		if err := rows.Scan(&skillIDStr, &bonus, &bonusText, &unitIDStr, &skillName); err != nil {
			continue
		}
		text := ""
		if bonusText != nil {
			text = cleanTraitText(*bonusText)
		}

		sid := safeIntText(skillIDStr)
		entry := map[string]any{
			"skill_name": nil,
			"skill_id":   nil,
			"bonus":      safeFloatText(bonus),
			"unit_id":    safeIntText(unitIDStr),
			"text":       text,
		}
		if sid != nil && *sid > 0 {
			entry["skill_id"] = *sid
			if skillName != nil {
				entry["skill_name"] = *skillName
			}
		}
		out = append(out, entry)
	}
	if out == nil {
		return []map[string]any{}
	}
	return out
}

// cleanTraitText turns a raw invTraits.bonusText into plain text: the
// <a href=showinfo:...>label</a> wrappers are stripped (the label is kept) and any
// other tag is dropped.
func cleanTraitText(raw string) string {
	text := strings.TrimSpace(raw)
	text = _reAnchorTag.ReplaceAllString(text, "$1")
	text = _reHTMLTag.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// ShipTraitTexts returns the bonus texts of every published ship hull's invTraits
// rows in one query, keyed by the hull's typeName, each text cleaned exactly like
// GetShipTraits cleans it (anchors stripped), in trait order. Hulls without trait
// rows are absent; the map is empty when the SDE has no invTraits table. It lets a
// caller that needs MANY hulls (the fit-search weapon-affinity lookup) avoid one
// GetShipTraits round-trip per hull.
func (s *SDE) ShipTraitTexts() map[string][]string {
	out := map[string][]string{}
	if !s.HasTable("invTraits") {
		return out
	}
	rows, err := s.db.Query(`
		SELECT inv.typeName, t.bonusText
		FROM invTraits t
		JOIN invTypes inv ON inv.typeID = CAST(t.typeID AS INTEGER)
		JOIN invGroups g ON g.groupID = inv.groupID
		WHERE g.categoryID = 6 AND inv.published = 1
		ORDER BY inv.typeID, CAST(t.traitID AS INTEGER)`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var bonusText *string
		if err := rows.Scan(&name, &bonusText); err != nil || bonusText == nil {
			continue
		}
		if text := cleanTraitText(*bonusText); text != "" {
			out[name] = append(out[name], text)
		}
	}
	return out
}

// GetItemMetaTier returns the meta-group label for an item.
func (s *SDE) GetItemMetaTier(typeID int) *string {
	if !s.HasTable("invMetaTypes") || !s.HasTable("invMetaGroups") {
		return nil
	}
	var name string
	err := s.db.QueryRow(`
		SELECT mg.metaGroupName
		FROM invMetaTypes mt
		JOIN invMetaGroups mg
			ON CAST(mg.metaGroupID AS INTEGER) = CAST(mt.metaGroupID AS INTEGER)
		WHERE CAST(mt.typeID AS INTEGER) = ?`, typeID).Scan(&name)
	if err == nil && name != "" {
		return &name
	}
	// Fallback: confirm typeID exists, assume Tech I
	var dummy int
	err2 := s.db.QueryRow("SELECT 1 FROM invTypes WHERE typeID = ?", typeID).Scan(&dummy)
	if err2 != nil {
		return nil
	}
	t1 := "Tech I"
	return &t1
}

// FindCanonicalModule returns canonical published SDE typeNames matching a module family.
func (s *SDE) FindCanonicalModule(family string, size *string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 8
	}
	family = strings.TrimSpace(family)
	if family == "" {
		return nil
	}

	params := []any{"%" + family + "%"}
	sizeClause := ""
	if size != nil {
		sz := capitalize(strings.TrimSpace(*size))
		sizeClause = " AND (typeName LIKE ? OR typeName LIKE ?)"
		params = append(params, sz+" %", "% "+sz+" %")
		// Steel Plates: Large=1600mm, Medium=800mm, Small=200mm
		if strings.HasSuffix(strings.ToLower(family), "steel plates") {
			mmMap := map[string]string{"Small": "200mm", "Medium": "800mm", "Large": "1600mm"}
			if mm, ok := mmMap[sz]; ok {
				sizeClause = " AND (typeName LIKE ? OR typeName LIKE ?)"
				params = []any{"%" + family + "%", "%" + mm + "%", "%" + mm + "%"}
			}
		}
	}
	params = append(params, limit*2)

	sql := "SELECT typeID, typeName FROM invTypes " +
		"WHERE typeName LIKE ? AND published=1" + sizeClause +
		" ORDER BY LENGTH(typeName), typeID LIMIT ?"

	rows, err := s.db.Query(sql, params...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		low := strings.ToLower(name)
		if strings.HasSuffix(low, " blueprint") || strings.Contains(low, " skin") ||
			strings.Contains(low, "specialization") || strings.Contains(low, "mutaplasmid") ||
			strings.HasSuffix(low, " operation") {
			continue
		}
		out = append(out, map[string]any{"type_id": id, "name": name})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// GetHullFacts returns an authoritative SDE snapshot of a hull's fitting envelope.
// The community_fit_example is NOT part of this SDE snapshot — it is appended at the
// tools layer (tools.fetchCommunityFitExample) from the rag retriever, matching the
// Python split where _fetch_community_fit_example lives in tools.py, not sde.py.
// ResolveShipName resolves a partial or loose hull name to the canonical published
// ship typeName (categoryID 6). It tries exact, then prefix, then substring matches —
// each preferring the shortest name (so "megath" → "Megathron", not "Megathron Navy
// Issue") — and finally a fuzzy fallback for typos, constrained to ship hulls. Returns
// nil when nothing ship-like matches.
func (s *SDE) ResolveShipName(partial string) *string {
	q := strings.TrimSpace(partial)
	if q == "" {
		return nil
	}
	for _, pat := range []string{q, q + "%", "%" + q + "%"} {
		var name string
		err := s.db.QueryRow(`
			SELECT inv.typeName
			FROM invTypes inv
			JOIN invGroups g ON g.groupID = inv.groupID
			WHERE g.categoryID = 6 AND inv.published = 1
			  AND lower(inv.typeName) LIKE lower(?)
			ORDER BY length(inv.typeName) ASC
			LIMIT 1`, pat).Scan(&name)
		if err == nil && name != "" {
			return &name
		}
	}
	// Fuzzy fallback (typo tolerance), constrained to ship hulls.
	for _, m := range s.FuzzyMatch(q, 5, 0.55) {
		id, _ := m[0].(int)
		name, _ := m[1].(string)
		if name == "" {
			continue
		}
		var cat int
		if s.db.QueryRow(
			`SELECT g.categoryID FROM invTypes inv JOIN invGroups g ON g.groupID = inv.groupID WHERE inv.typeID = ?`,
			id).Scan(&cat) == nil && cat == 6 {
			return &name
		}
	}
	return nil
}

func (s *SDE) GetHullFacts(shipName string) map[string]any {
	if shipName == "" {
		return nil
	}
	clean := strings.TrimSpace(shipName)
	ids := s.ResolveNames([]string{clean})
	typeID, ok := ids[clean]
	if !ok {
		fuzzy := s.FuzzyMatch(clean, 1, 0.6)
		if len(fuzzy) == 0 {
			return nil
		}
		typeID = fuzzy[0][0].(int)
	}

	var typeName string
	var groupName, categoryName *string
	err := s.db.QueryRow(`
		SELECT inv.typeName, g.groupName, c.categoryName
		FROM invTypes inv
		LEFT JOIN invGroups g ON g.groupID = inv.groupID
		LEFT JOIN invCategories c ON c.categoryID = g.categoryID
		WHERE inv.typeID = ?`, typeID).Scan(&typeName, &groupName, &categoryName)
	if err != nil {
		name := s.GetTypeName(typeID)
		if name == nil {
			return nil
		}
		typeName = *name
	}
	gname := ""
	if groupName != nil {
		gname = *groupName
	}
	cname := ""
	if categoryName != nil {
		cname = *categoryName
	}

	// Slot / hardpoint / drone-bay attributes
	attrIDs := []any{typeID, 14, 13, 12, 1137, 102, 101, 283, 1271}
	rows, err := s.db.Query(
		"SELECT attributeID, COALESCE(valueInt, valueFloat) AS v "+
			"FROM dgmTypeAttributes WHERE typeID = ? AND attributeID IN (?,?,?,?,?,?,?,?)",
		attrIDs...)
	attrs := map[int]float64{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var aid int
			var val *float64
			if err := rows.Scan(&aid, &val); err == nil && val != nil {
				attrs[aid] = *val
			}
		}
	}

	asInt := func(v float64) int { return int(v) }
	asFloat := func(v float64) float64 { return v }

	slots := map[string]any{
		"hi":  asInt(attrs[14]),
		"mid": asInt(attrs[13]),
		"low": asInt(attrs[12]),
		"rig": asInt(attrs[1137]),
	}
	hardpoints := map[string]any{
		"turret":   asInt(attrs[102]),
		"launcher": asInt(attrs[101]),
	}
	droneBay := map[string]any{
		"capacity_m3":    asFloat(attrs[283]),
		"bandwidth_mbit": asFloat(attrs[1271]),
	}

	// Per-hull restricted weapons
	weapRows, err := s.db.Query(`
		SELECT DISTINCT inv.typeID, inv.typeName
		FROM dgmTypeAttributes dta
		JOIN invTypes inv ON inv.typeID = dta.typeID
		WHERE dta.attributeID IN (1298,1299,1300,1301,1302)
		  AND CAST(COALESCE(dta.valueInt, dta.valueFloat) AS INTEGER) = ?
		  AND inv.published = 1
		ORDER BY LENGTH(inv.typeName), inv.typeID`, typeID)
	var compatWeapons []map[string]any
	if err == nil {
		defer weapRows.Close()
		for weapRows.Next() {
			var wid int
			var wname string
			if err := weapRows.Scan(&wid, &wname); err != nil {
				continue
			}
			low := strings.ToLower(wname)
			if strings.HasSuffix(low, " blueprint") || strings.Contains(low, " skin") {
				continue
			}
			compatWeapons = append(compatWeapons, map[string]any{"type_id": wid, "name": wname})
		}
	}
	if compatWeapons == nil {
		compatWeapons = []map[string]any{}
	}

	roleBonuses := s.GetShipTraits(typeID)
	hullClass := s.GetShipClass(typeID)
	var sizePrefix *string
	if hullClass != nil {
		if sp, ok := _shipClassSizePrefix[*hullClass]; ok {
			sizePrefix = &sp
		}
	}

	seenFamilies := map[string]bool{}
	var bonusedModules []map[string]any
	if len(roleBonuses) > 0 {
		texts := make([]string, 0, len(roleBonuses))
		for _, t := range roleBonuses {
			if txt, ok := t["text"].(string); ok {
				texts = append(texts, txt)
			}
		}
		joined := strings.ToLower(strings.Join(texts, "\n"))
		for _, be := range _bonusTextToFamily {
			if strings.Contains(joined, be.keyword) && !seenFamilies[be.family] {
				seenFamilies[be.family] = true
				var sp *string
				if be.sized {
					sp = sizePrefix
				}
				canonicals := s.FindCanonicalModule(be.family, sp, 5)
				if len(canonicals) > 0 {
					names := make([]string, 0, len(canonicals))
					for _, c := range canonicals {
						names = append(names, c["name"].(string))
					}
					entry := map[string]any{
						"trigger":   be.keyword,
						"family":    be.family,
						"size":      nil,
						"canonical": names,
					}
					if be.sized && sizePrefix != nil {
						entry["size"] = *sizePrefix
					}
					bonusedModules = append(bonusedModules, entry)
				}
			}
		}
	}
	if bonusedModules == nil {
		bonusedModules = []map[string]any{}
	}

	var hullClassVal any
	if hullClass != nil {
		hullClassVal = *hullClass
	}

	return map[string]any{
		"ship_id":            typeID,
		"ship_name":          typeName,
		"hull_class":         hullClassVal,
		"group":              gname,
		"category":           cname,
		"slots":              slots,
		"hardpoints":         hardpoints,
		"drone_bay":          droneBay,
		"role_bonuses":       roleBonuses,
		"compatible_weapons": compatWeapons,
		"bonused_modules":    bonusedModules,
	}
}

// safeIntText converts a *string (from generic TEXT column) to *int.
func safeIntText(v *string) *int {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" || strings.ToLower(s) == "none" {
		return nil
	}
	// Parse as float first to handle "3.0" style
	var f float64
	if _, err := parseFloat(s, &f); err != nil {
		return nil
	}
	n := int(f)
	return &n
}

// safeFloatText converts a *string (from generic TEXT column) to *float64.
func safeFloatText(v *string) *float64 {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" || strings.ToLower(s) == "none" {
		return nil
	}
	var f float64
	if _, err := parseFloat(s, &f); err != nil {
		return nil
	}
	return &f
}

func parseFloat(s string, f *float64) (int, error) {
	n, err := fmt.Sscanf(s, "%g", f)
	return n, err
}

// capitalize upper-cases the first letter of an ASCII string (EVE item names are ASCII).
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// AllShipNames returns every published ship hull typeName (categoryID 6),
// unordered. Callers that need to scan free text for a hull mention (see
// chat/brain.extractHullName) should cache this once — the underlying query
// touches every row in invTypes joined to invGroups.
func (s *SDE) AllShipNames() []string {
	rows, err := s.db.Query(`
		SELECT DISTINCT t.typeName
		FROM invTypes t
		JOIN invGroups g ON t.groupID = g.groupID
		WHERE g.categoryID = 6 AND t.published = 1 AND t.typeName <> ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		out = append(out, name)
	}
	return out
}
