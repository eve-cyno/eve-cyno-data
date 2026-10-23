package sde

import (
	"database/sql"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
)

// SystemFacts is the static profile of one solar system, built once per SDE generation.
//
// The Neighbours and StationServices slices and the PlanetsByType map are shared with the
// per-generation cache that SystemFactsByID and FindSystems copy from: treat them as read-only.
type SystemFacts struct {
	// ID is the solarSystemID.
	ID int
	// Name is the canonical system name.
	Name string
	// Security is the raw true security status; use DisplaySecurity for the value the game shows.
	Security float64
	// RegionID is the region's ID.
	RegionID int
	// Region is the region name.
	Region string
	// ConstellationID is the constellation's ID.
	ConstellationID int
	// Constellation is the constellation name.
	Constellation string
	// FactionID is the system's own factionID, else its constellation's, else its region's (0 = none).
	FactionID int
	// Faction is the name of FactionID ("" = none).
	Faction string
	// WormholeClass is the class of the system, else of its constellation, else of its region (0 = none).
	WormholeClass int
	// WormholeEffect is the name of the system's secondary sun ("" = none).
	WormholeEffect string
	// Neighbours are the stargate destinations, sorted by name.
	Neighbours []int
	// Belts is the number of asteroid belts.
	Belts int
	// Moons is the number of moons.
	Moons int
	// Planets is the number of planets.
	Planets int
	// Stargates is the number of stargates.
	Stargates int
	// PlanetsByType counts planets per type, keyed by the type name without the "Planet (" prefix.
	PlanetsByType map[string]int
	// NPCStations is the number of NPC stations.
	NPCStations int
	// StationServices is the sorted union of the services of the system's NPC stations.
	StationServices []string
}

// NearFilter ranks and optionally bounds systems by gate jumps from Origin.
type NearFilter struct {
	// Origin is the solarSystemID the jumps are counted from.
	Origin int
	// MaxJumps keeps systems at most this many jumps away; 0 = no radius.
	MaxJumps int
	// PreferHighSec counts jumps through highsec where a highsec route exists, else through any space.
	PreferHighSec bool
}

// JumpRangeFilter keeps the systems Ship can jump to from Origin within RangeLY (SystemsInJumpRange).
type JumpRangeFilter struct {
	// Origin is the solarSystemID the jump starts in; it is never part of the result.
	Origin int
	// Ship is the hull (or the Ansiblex) whose valid jump targets are kept.
	Ship JumpShip
	// RangeLY is the jump range in light-years.
	RangeLY float64
}

// SystemFilter selects systems; zero values mean "any".
type SystemFilter struct {
	// SecMin keeps systems whose displayed security (DisplaySecurity) is at least this; nil = any.
	SecMin *float64
	// SecMax keeps systems whose displayed security (DisplaySecurity) is at most this; nil = any.
	SecMax *float64
	// RegionID keeps one region's systems; 0 = any.
	RegionID int
	// ConstellationID keeps one constellation's systems; 0 = any.
	ConstellationID int
	// FactionID keeps systems of one faction (SystemFacts.FactionID); 0 = any.
	FactionID int
	// WormholeClass keeps systems of one wormhole class; 0 = any.
	WormholeClass int
	// HasNPCStation keeps systems with (true) or without (false) NPC stations; nil = any.
	HasNPCStation *bool
	// StationServices are canonical service names; a system must offer all of them.
	StationServices []string
	// MinBelts keeps systems with at least this many asteroid belts.
	MinBelts int
	// PlanetTypes are canonical names; a system must have at least one planet of each.
	PlanetTypes []string
	// OnlyIDs restricts the result to these systems; nil = no restriction.
	OnlyIDs map[int]bool
	// Near ranks by jumps from an origin and leaves out systems without a gate route; nil = no ranking.
	Near *NearFilter
	// JumpRange keeps the systems a ship can jump to from an origin and ranks them by light-years; nil = no jump filter.
	JumpRange *JumpRangeFilter
	// Sort is "jumps", "security", "name" or "ly" ("" = ly with JumpRange, else jumps with Near, else name;
	// an unknown value sorts by name).
	Sort string
}

// SystemMatch is one FindSystems row.
type SystemMatch struct {
	SystemFacts
	// Jumps is the gate distance from Near.Origin, -1 without Near.
	Jumps int
	// HighsecRoute is true only when Near.PreferHighSec was set and a highsec-only route exists
	// to the system; it is false otherwise, including for an all-highsec route found without PreferHighSec.
	HighsecRoute bool
	// LY is the distance in light-years from JumpRange.Origin, -1 without JumpRange.
	LY float64
}

// systemIndex is the per-generation system profile cache.
type systemIndex struct {
	gen      uint64
	byID     map[int]*SystemFacts
	ordered  []*SystemFacts // sorted by name
	services []string
	planets  []string
}

// SystemFactsByID returns the profile of a system.
func (s *SDE) SystemFactsByID(id int) (SystemFacts, bool) {
	f, ok := s.ensureSystemIndex().byID[id]
	if !ok {
		return SystemFacts{}, false
	}
	return *f, true
}

// StationServiceNames is the sorted vocabulary of NPC station services.
func (s *SDE) StationServiceNames() []string {
	return append([]string(nil), s.ensureSystemIndex().services...)
}

// PlanetTypeNames is the sorted vocabulary of planet types ("Gas", "Ice", ...).
func (s *SDE) PlanetTypeNames() []string {
	return append([]string(nil), s.ensureSystemIndex().planets...)
}

// ResolveConstellationName case-insensitively maps a constellation name to its ID.
func (s *SDE) ResolveConstellationName(name string) *int {
	return s.resolveByName("mapConstellations", "constellationID", "constellationName", name)
}

// ResolveFactionName case-insensitively maps a faction name to its ID.
func (s *SDE) ResolveFactionName(name string) *int {
	return s.resolveByName("chrFactions", "factionID", "factionName", name)
}

func (s *SDE) resolveByName(table, idCol, nameCol, name string) *int {
	if !s.HasTable(table) {
		return nil
	}
	var id int
	err := s.db.QueryRow("SELECT "+idCol+" FROM "+table+" WHERE "+nameCol+" = ? COLLATE NOCASE", strings.TrimSpace(name)).Scan(&id)
	if err != nil {
		return nil
	}
	return &id
}

// FindSystems returns every system matching f, sorted; the caller pages.
func (s *SDE) FindSystems(f SystemFilter) []SystemMatch {
	idx := s.ensureSystemIndex()
	var dist, hsDist map[int]int
	if f.Near != nil {
		dist = s.JumpDistances(f.Near.Origin, false, nil)
		if f.Near.PreferHighSec {
			hsDist = s.JumpDistances(f.Near.Origin, true, nil)
		}
	}
	var lyTo map[int]float64
	if f.JumpRange != nil {
		lyTo = s.SystemsInJumpRange(f.JumpRange.Origin, f.JumpRange.Ship, f.JumpRange.RangeLY)
	}
	out := []SystemMatch{}
	for _, sf := range idx.ordered {
		if !matchesSystem(sf, f) {
			continue
		}
		m := SystemMatch{SystemFacts: *sf, Jumps: -1, LY: -1}
		if f.JumpRange != nil {
			d, ok := lyTo[sf.ID]
			if !ok {
				continue
			}
			m.LY = d
		}
		if f.Near != nil {
			d, ok := dist[sf.ID]
			if hd, hok := hsDist[sf.ID]; hok {
				d, ok, m.HighsecRoute = hd, true, true
			}
			if !ok || (f.Near.MaxJumps > 0 && d > f.Near.MaxJumps) {
				continue
			}
			m.Jumps = d
		}
		out = append(out, m)
	}
	mode := f.Sort
	if mode == "" {
		mode = "name"
		switch {
		case f.JumpRange != nil:
			mode = "ly"
		case f.Near != nil:
			mode = "jumps"
		}
	}
	// out is name-ordered; the stable sorts keep that as the tie-break.
	switch mode {
	case "jumps":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Jumps < out[j].Jumps })
	case "security":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Security > out[j].Security })
	case "ly":
		sort.SliceStable(out, func(i, j int) bool { return out[i].LY < out[j].LY })
	}
	return out
}

func matchesSystem(sf *SystemFacts, f SystemFilter) bool {
	if f.OnlyIDs != nil && !f.OnlyIDs[sf.ID] {
		return false
	}
	if f.SecMin != nil && DisplaySecurity(sf.Security) < *f.SecMin {
		return false
	}
	if f.SecMax != nil && DisplaySecurity(sf.Security) > *f.SecMax {
		return false
	}
	if f.RegionID != 0 && sf.RegionID != f.RegionID {
		return false
	}
	if f.ConstellationID != 0 && sf.ConstellationID != f.ConstellationID {
		return false
	}
	if f.FactionID != 0 && sf.FactionID != f.FactionID {
		return false
	}
	if f.WormholeClass != 0 && sf.WormholeClass != f.WormholeClass {
		return false
	}
	if f.HasNPCStation != nil && (sf.NPCStations > 0) != *f.HasNPCStation {
		return false
	}
	if sf.Belts < f.MinBelts {
		return false
	}
	for _, want := range f.StationServices {
		if !sliceContains(sf.StationServices, want) {
			return false
		}
	}
	for _, p := range f.PlanetTypes {
		if sf.PlanetsByType[p] == 0 {
			return false
		}
	}
	return true
}

func (s *SDE) ensureSystemIndex() *systemIndex {
	h, err := s.db.acquire()
	if err != nil {
		return &systemIndex{byID: map[int]*SystemFacts{}}
	}
	defer h.release()
	s.factsMu.Lock()
	defer s.factsMu.Unlock()
	if s.facts != nil && s.facts.gen == h.gen {
		return s.facts
	}
	s.facts = buildSystemIndex(h.db, h.gen, s.ensureMapCaches())
	return s.facts
}

func textInt(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// idNames reads an ID/name query; a failure yields an empty map.
func idNames(db *sql.DB, q string) map[int]string {
	m := map[int]string{}
	eachRow(db, q, q, func(r *sql.Rows) error {
		var id int
		var name sql.NullString
		if err := r.Scan(&id, &name); err != nil {
			return err
		}
		m[id] = name.String
		return nil
	})
	return m
}

// idInts reads an ID/nullable-int query into a map, skipping NULL values.
func idInts(db *sql.DB, q string) map[int]int {
	m := map[int]int{}
	eachRow(db, q, q, func(r *sql.Rows) error {
		var id int
		var v sql.NullInt64
		if err := r.Scan(&id, &v); err != nil {
			return err
		}
		if v.Valid {
			m[id] = int(v.Int64)
		}
		return nil
	})
	return m
}

// DisplaySecurity returns a security status as the game shows it: rounded to one decimal,
// except that a positive value below 0.05 shows as 0.1 (the system is still lowsec) and a value
// that rounds to zero is 0.0, never negative zero (the client shows 0.0; "+0.0" is this project's %+.1f style).
func DisplaySecurity(raw float64) float64 {
	if raw > 0 && raw < 0.05 {
		return 0.1
	}
	d := math.Round(raw*10) / 10
	if d == 0 {
		return 0 // never negative zero: a true security of -0.004 is 0
	}
	return d
}

// eachRow runs q and calls fn per row; a query, scan or iteration failure is logged and
// the rows read so far stay in effect (the index is then partial, as before). Row failures log
// once per query: the first error and the count.
func eachRow(db *sql.DB, name, q string, fn func(*sql.Rows) error) {
	rows, err := db.Query(q)
	if err != nil {
		slog.Warn("sde index: query failed", "query", name, "error", err)
		return
	}
	defer rows.Close()
	var first error
	failed := 0
	for rows.Next() {
		if err := fn(rows); err != nil {
			if first == nil {
				first = err
			}
			failed++
		}
	}
	if failed > 0 {
		slog.Warn("sde index: scan failed", "query", name, "error", first, "failed_rows", failed)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("sde index: iteration failed", "query", name, "error", err)
	}
}

// unenterableRegions are regions whose systems players cannot reach: the three gateless Jove
// regions (UUA-F4, J7HZ-F, A821-A; no stargate connects them to the map, 13 of the UUA-F4
// systems have gates among themselves and carry wormhole classes 10 and 11) and GPMR-01, the
// pseudo-region of the global PLEX market (system GPMS-01). Pochven is not listed: its systems
// are entered through filaments.
func unenterableRegion(regionID int) bool {
	switch regionID {
	case 10000004, 10000017, 10000019, 19000001:
		return true
	}
	return false
}

// NotEnterable reports whether the system is in a region players cannot enter (the gateless Jove
// regions and the PLEX market pseudo-region). Such a system is in the SDE but not in the system
// index, so lookups answer it apart from an unknown name.
func (s *SDE) NotEnterable(systemID int) bool {
	region, _ := s.GetSystemMetadata(systemID)["region_id"].(int)
	return unenterableRegion(region)
}

// isDeadspacePseudoRegion reports whether a region ID is in the 12000000-14999999 block: the Abyssal
// deadspace (ADR01-05, AD001-AD200) and Void (V-001-V-200) pseudo-systems. They have no map
// position, no gates and a fixed -0.99 security, so they are not map systems.
func isDeadspacePseudoRegion(regionID int) bool {
	return regionID >= 12000000 && regionID < 15000000
}

func buildSystemIndex(db *sql.DB, gen uint64, mc *mapCaches) *systemIndex {
	idx := &systemIndex{gen: gen, byID: map[int]*SystemFacts{}}
	regions := idNames(db, "SELECT regionID, regionName FROM mapRegions")
	consts := idNames(db, "SELECT constellationID, constellationName FROM mapConstellations")
	factions := idNames(db, "SELECT factionID, factionName FROM chrFactions")
	regionFaction := idInts(db, "SELECT regionID, factionID FROM mapRegions")
	constFaction := idInts(db, "SELECT constellationID, factionID FROM mapConstellations")

	// Wormhole classes are keyed by system, constellation or region ID (all in one ID space).
	wh := map[int]int{}
	eachRow(db, "wormhole classes", "SELECT locationID, wormholeClassID FROM mapLocationWormholeClasses", func(r *sql.Rows) error {
		var l, c sql.NullString
		if err := r.Scan(&l, &c); err != nil {
			return err
		}
		wh[textInt(l.String)] = textInt(c.String)
		return nil
	})

	eachRow(db, "systems", "SELECT solarSystemID, regionID, constellationID, solarSystemName, security, factionID FROM mapSolarSystems", func(r *sql.Rows) error {
		var f SystemFacts
		var name sql.NullString
		var sec sql.NullFloat64
		var fid sql.NullInt64
		if err := r.Scan(&f.ID, &f.RegionID, &f.ConstellationID, &name, &sec, &fid); err != nil {
			return err
		}
		f.Name, f.Security = name.String, sec.Float64
		if isDeadspacePseudoRegion(f.RegionID) || unenterableRegion(f.RegionID) {
			return nil
		}
		f.Region, f.Constellation = regions[f.RegionID], consts[f.ConstellationID]
		switch {
		case fid.Valid && fid.Int64 != 0:
			f.FactionID = int(fid.Int64)
		case constFaction[f.ConstellationID] != 0:
			f.FactionID = constFaction[f.ConstellationID]
		default:
			f.FactionID = regionFaction[f.RegionID]
		}
		f.Faction = factions[f.FactionID]
		for _, id := range []int{f.ID, f.ConstellationID, f.RegionID} {
			if c := wh[id]; c != 0 {
				f.WormholeClass = c
				break
			}
		}
		f.PlanetsByType = map[string]int{}
		f.StationServices = []string{}
		f.Neighbours = []int{}
		idx.byID[f.ID] = &f
		return nil
	})

	// Celestials: the columns are TEXT in the generic table, so they are cast in Go.
	typeNames := idNames(db, "SELECT typeID, typeName FROM invTypes WHERE groupID IN (7, 995)")
	planetVocab := map[string]bool{}
	for _, n := range typeNames {
		if strings.HasPrefix(n, "Planet (") {
			planetVocab[strings.TrimSuffix(strings.TrimPrefix(n, "Planet ("), ")")] = true
		}
	}
	eachRow(db, "celestials", "SELECT solarSystemID, groupID, typeID, count(*) FROM mapDenormalize WHERE groupID IN ('7','8','9','10','995') GROUP BY 1,2,3", func(r *sql.Rows) error {
		var sys, grp, typ sql.NullString
		var n int
		if err := r.Scan(&sys, &grp, &typ, &n); err != nil {
			return err
		}
		f := idx.byID[textInt(sys.String)]
		if f == nil {
			return nil
		}
		switch grp.String {
		case "7":
			f.Planets += n
			name := strings.TrimSuffix(strings.TrimPrefix(typeNames[textInt(typ.String)], "Planet ("), ")")
			if name != "" {
				f.PlanetsByType[name] += n
			}
		case "8":
			f.Moons += n
		case "9":
			f.Belts += n
		case "10":
			f.Stargates += n
		case "995":
			f.WormholeEffect = typeNames[textInt(typ.String)]
		}
		return nil
	})
	idx.planets = sortedKeys(planetVocab)

	// Stargate neighbours, sorted by name.
	for from, tos := range mc.adj {
		f := idx.byID[from]
		if f == nil {
			continue
		}
		nb := append([]int(nil), tos...)
		sort.Slice(nb, func(i, j int) bool {
			a, b := idx.byID[nb[i]], idx.byID[nb[j]]
			if a == nil || b == nil {
				return nb[i] < nb[j]
			}
			return a.Name < b.Name
		})
		f.Neighbours = nb
	}

	// NPC stations and the union of their services.
	svcNames := idNames(db, "SELECT serviceID, serviceName FROM staServices")
	opServices := map[int][]string{}
	eachRow(db, "operation services", "SELECT operationID, serviceID FROM staOperationServices", func(r *sql.Rows) error {
		var op, sv int
		if err := r.Scan(&op, &sv); err != nil {
			return err
		}
		if svcNames[sv] != "" {
			opServices[op] = append(opServices[op], svcNames[sv])
		}
		return nil
	})
	vocab := map[string]bool{}
	for _, n := range svcNames {
		if n != "" {
			vocab[n] = true
		}
	}
	idx.services = sortedKeys(vocab)
	sysServices := map[int]map[string]bool{}
	eachRow(db, "stations", "SELECT solarSystemID, operationID FROM staStations", func(r *sql.Rows) error {
		var sys int
		var op sql.NullInt64
		if err := r.Scan(&sys, &op); err != nil {
			return err
		}
		f := idx.byID[sys]
		if f == nil {
			return nil
		}
		f.NPCStations++
		if sysServices[sys] == nil {
			sysServices[sys] = map[string]bool{}
		}
		for _, n := range opServices[int(op.Int64)] {
			sysServices[sys][n] = true
		}
		return nil
	})
	for sys, set := range sysServices {
		idx.byID[sys].StationServices = sortedKeys(set)
	}

	for _, f := range idx.byID {
		idx.ordered = append(idx.ordered, f)
	}
	sort.Slice(idx.ordered, func(i, j int) bool {
		if idx.ordered[i].Name != idx.ordered[j].Name {
			return idx.ordered[i].Name < idx.ordered[j].Name
		}
		return idx.ordered[i].ID < idx.ordered[j].ID
	})
	return idx
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
