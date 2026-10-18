package sde

import (
	"container/heap"
	"database/sql"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
)

// LightYear is the number of metres in a light-year as EVE route tools measure it.
const LightYear = 9.46e15

// Fixed IDs of the jump rules. Attribute and type IDs are the SDE's own; the rules they feed are
// the facts of the EVE University wiki pages "Jump drives", "Pochven" and "Zarzakh".
const (
	jitaSystemID    = 30000142 // the gate network of known space is the component of Jita
	zarzakhSystemID = 30100000 // a jump drive cannot end a hop in Zarzakh
	pochvenClass    = 25       // wormhole class of Pochven: jump drives can leave it but not enter it
	ansiblexTypeID  = 35841    // Ansiblex Jump Bridge (not the retired POS jump bridge, type 27897)

	jumpCalibrationSkillID  = 21611 // Jump Drive Calibration
	jumpFuelSavingSkillID   = 21610 // Jump Fuel Conservation
	attrJumpDriveRange      = 867
	attrJumpDriveConsume    = 868
	attrJumpDriveFuelType   = 866
	attrJumpRangeBonus      = 870  // jumpDriveRangeBonus, per level of Jump Drive Calibration
	attrFuelConsumeBonus    = 1296 // consumptionQuantityBonusPercentage, per level of Jump Fuel Conservation
	attrJumpFreighterBonus  = 1312 // eliteBonusJumpFreighter2, per level of Jump Freighters
	attrDisallowInHighSec   = 1970
	attrEnableOpenJumpPort  = 3125 // enableOpenJumpPortal: the module opens a bridge
	attrEnableConduitJump   = 3126 // enablePerformConduitJump
	highsecDisplayThreshold = 0.5  // a cynosural field cannot be lit in highsec
)

// canFitShipGroupAttrs are canFitShipGroup01..20: the ship groups a module fits.
var canFitShipGroupAttrs = []int{1298, 1299, 1300, 1301, 1872, 1879, 1880, 1881, 2065, 2396, 2476, 2477, 2478, 2479, 2480, 2481, 2482, 2483, 2484, 2485}

// JumpShip is a hull with a jump drive, or the Ansiblex jump bridge.
type JumpShip struct {
	TypeID             int
	Name               string  // SDE type name: "Rhea", "Ansiblex Jump Bridge"
	Group              string  // SDE group name: "Jump Freighter", "Upwell Jump Bridge"
	BaseRangeLY        float64 // jumpDriveRange before skills
	FuelTypeID         int     // 0 for the Ansiblex
	FuelName           string  // "Nitrogen Isotopes"; "" for the Ansiblex
	FuelUnitVolume     float64 // m³ per unit (0.03)
	BaseFuelPerLY      float64 // jumpDriveConsumptionAmount before skills; 0 for the Ansiblex
	JumpFreighterBonus float64 // eliteBonusJumpFreighter2 per Jump Freighters level (-10); 0 if none
	NoHighsec          bool    // disallowInHighSec
	CanBridge          bool    // a jump portal generator fits the hull's group
	CanConduit         bool    // the hull or its portal generator enables conduit jumps
	Ansiblex           bool    // fixed range, no skills, no ship fuel, ends only in player-sovereign nullsec
}

// JumpSkills are skill levels 0–5.
type JumpSkills struct{ Calibration, FuelConservation, JumpFreighters int }

// JumpPlace says whether a system can start or end a jump, and why not.
type JumpPlace int

const (
	JumpOK            JumpPlace = iota
	JumpHighsec                 // displayed security >= 0.5: no cyno; a NoHighsec hull cannot be there
	JumpWormhole                // wormhole space (classes 1–6, 12–18): no jump drives
	JumpPochven                 // may start a route, never ends a hop
	JumpZarzakh                 // never ends a hop
	JumpOffNetwork              // not on known space's stargate network
	JumpNoSovereignty           // Ansiblex only: not nullsec under possible player sovereignty
)

// JumpRouteOptions shapes PlanJumpRoute.
type JumpRouteOptions struct {
	RangeLY        float64
	Avoid          map[int]bool // systems no hop ends in (the stops are exempt)
	AvoidRegions   map[int]bool // regions no hop ends in (the stops are exempt)
	PreferStations bool         // among the routes with the fewest jumps, fewest hops ending without an NPC station
}

// JumpStep is one hop of a planned route.
type JumpStep struct {
	From, To int
	LY       float64
}

// jumpPos is a system's position in metres.
type jumpPos struct{ x, y, z float64 }

// jumpIndex is the per-generation jump data: positions, the gate network, the hulls and the two skill bonuses.
type jumpIndex struct {
	gen      uint64
	pos      map[int]jumpPos
	network  map[int]bool // the gate component of Jita
	ships    []JumpShip   // sorted by Name
	cyno     []int        // systems a cynosural hop can end in, sorted by ID
	ansiblex []int        // systems an Ansiblex can stand in, sorted by ID
	calib    float64      // jumpDriveRangeBonus of Jump Drive Calibration, percent per level
	fuelSave float64      // consumptionQuantityBonusPercentage of Jump Fuel Conservation, percent per level
	facts    *systemIndex
}

func (s *SDE) ensureJumpIndex() *jumpIndex {
	// acquire() first, like ensureMapCaches: a due reload happens there and gen is current.
	h, err := s.db.acquire()
	if err != nil {
		return &jumpIndex{pos: map[int]jumpPos{}, network: map[int]bool{}, facts: &systemIndex{byID: map[int]*SystemFacts{}}}
	}
	defer h.release()
	s.jumpMu.Lock()
	defer s.jumpMu.Unlock()
	if s.jumps != nil && s.jumps.gen == h.gen {
		return s.jumps
	}
	facts := s.ensureSystemIndex()
	mc := s.ensureMapCaches()
	idx := buildJumpIndex(h.db, h.gen, facts, mc)
	if facts.gen == h.gen && mc.gen == h.gen {
		s.jumps = idx // a reload between the acquire and the sub-indexes would mix generations: serve it once, do not keep it
	}
	return idx
}

func buildJumpIndex(db *sql.DB, gen uint64, facts *systemIndex, mc *mapCaches) *jumpIndex {
	idx := &jumpIndex{gen: gen, pos: map[int]jumpPos{}, network: map[int]bool{}, facts: facts}

	eachRow(db, "jump positions", "SELECT solarSystemID, x, y, z FROM mapSolarSystems", func(r *sql.Rows) error {
		var id int
		var x, y, z sql.NullString
		if err := r.Scan(&id, &x, &y, &z); err != nil {
			return err
		}
		fx, ex := strconv.ParseFloat(strings.TrimSpace(x.String), 64)
		fy, ey := strconv.ParseFloat(strings.TrimSpace(y.String), 64)
		fz, ez := strconv.ParseFloat(strings.TrimSpace(z.String), 64)
		if ex == nil && ey == nil && ez == nil {
			idx.pos[id] = jumpPos{fx, fy, fz}
		}
		return nil
	})

	// The gate network of known space: breadth-first from Jita over the cached adjacency.
	if _, ok := facts.byID[jitaSystemID]; ok {
		idx.network[jitaSystemID] = true
		queue := []int{jitaSystemID}
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			for _, v := range mc.adj[u] {
				if !idx.network[v] {
					idx.network[v] = true
					queue = append(queue, v)
				}
			}
		}
	}

	idx.loadShips(db)

	for id := range idx.network {
		f := facts.byID[id]
		if f == nil || idx.pos[id] == (jumpPos{}) {
			continue
		}
		if jumpPlace(idx, f, false) == JumpOK {
			idx.cyno = append(idx.cyno, id)
		}
		if jumpPlace(idx, f, true) == JumpOK {
			idx.ansiblex = append(idx.ansiblex, id)
		}
	}
	sort.Ints(idx.cyno)
	sort.Ints(idx.ansiblex)
	return idx
}

// loadShips reads the jump hulls and the Ansiblex, their bridge and conduit abilities and the two skill bonuses.
func (idx *jumpIndex) loadShips(db *sql.DB) {
	wanted := []int{attrJumpDriveRange, attrJumpDriveConsume, attrJumpDriveFuelType, attrJumpRangeBonus, attrFuelConsumeBonus,
		attrJumpFreighterBonus, attrDisallowInHighSec, attrEnableOpenJumpPort, attrEnableConduitJump}
	wanted = append(wanted, canFitShipGroupAttrs...)
	in := make([]string, len(wanted))
	for i, a := range wanted {
		in[i] = strconv.Itoa(a)
	}
	attrs := map[int]map[int]float64{} // typeID -> attributeID -> value
	eachRow(db, "jump attributes", "SELECT typeID, attributeID, COALESCE(valueFloat, valueInt) FROM dgmTypeAttributes WHERE attributeID IN ("+strings.Join(in, ",")+")", func(r *sql.Rows) error {
		var t, a int
		var v sql.NullFloat64
		if err := r.Scan(&t, &a, &v); err != nil || !v.Valid {
			return err
		}
		if attrs[t] == nil {
			attrs[t] = map[int]float64{}
		}
		attrs[t][a] = v.Float64
		return nil
	})
	idx.calib = attrs[jumpCalibrationSkillID][attrJumpRangeBonus]
	idx.fuelSave = attrs[jumpFuelSavingSkillID][attrFuelConsumeBonus]

	// Modules that open a portal or enable conduit jumps, with the ship groups they fit.
	type module struct {
		groups  map[int]bool
		conduit bool
	}
	var bridges []module
	for t, a := range attrs {
		if a[attrEnableOpenJumpPort] <= 0 {
			continue
		}
		var published int
		var cat sql.NullInt64
		if err := db.QueryRow(`SELECT t.published, g.categoryID FROM invTypes t JOIN invGroups g ON g.groupID = t.groupID WHERE t.typeID = ?`, t).Scan(&published, &cat); err != nil || published != 1 || cat.Int64 != 7 {
			continue // a published module (category 7) only
		}
		m := module{groups: map[int]bool{}, conduit: a[attrEnableConduitJump] > 0}
		for _, g := range canFitShipGroupAttrs {
			if v, ok := a[g]; ok {
				m.groups[int(v)] = true
			}
		}
		bridges = append(bridges, m)
	}

	type hull struct {
		id, group int
		name, grp string
	}
	var hulls []hull
	eachRow(db, "jump hulls", `SELECT t.typeID, t.typeName, t.groupID, g.groupName FROM invTypes t JOIN invGroups g ON g.groupID = t.groupID
		WHERE t.published = 1 AND (g.categoryID = 6 OR t.typeID = `+strconv.Itoa(ansiblexTypeID)+`)`, func(r *sql.Rows) error {
		var h hull
		if err := r.Scan(&h.id, &h.name, &h.group, &h.grp); err != nil {
			return err
		}
		hulls = append(hulls, h)
		return nil
	})
	for _, h := range hulls {
		a := attrs[h.id]
		if a[attrJumpDriveRange] <= 0 {
			continue
		}
		sh := JumpShip{TypeID: h.id, Name: h.name, Group: h.grp, BaseRangeLY: a[attrJumpDriveRange]}
		if h.id == ansiblexTypeID {
			sh.Ansiblex = true
		} else {
			sh.FuelTypeID = int(a[attrJumpDriveFuelType])
			sh.BaseFuelPerLY = a[attrJumpDriveConsume]
			sh.JumpFreighterBonus = a[attrJumpFreighterBonus]
			sh.NoHighsec = a[attrDisallowInHighSec] == 1
			sh.CanConduit = a[attrEnableConduitJump] > 0
			if sh.FuelTypeID != 0 {
				var vol sql.NullFloat64
				if err := db.QueryRow(`SELECT typeName, volume FROM invTypes WHERE typeID = ?`, sh.FuelTypeID).Scan(&sh.FuelName, &vol); err != nil {
					slog.Warn("sde jump index: fuel type", "type", sh.FuelTypeID, "error", err)
				}
				sh.FuelUnitVolume = vol.Float64
			}
			for _, m := range bridges {
				if m.groups[h.group] {
					sh.CanBridge = true
					if m.conduit {
						sh.CanConduit = true
					}
				}
			}
		}
		idx.ships = append(idx.ships, sh)
	}
	sort.Slice(idx.ships, func(i, j int) bool { return idx.ships[i].Name < idx.ships[j].Name })
}

// JumpShips lists every hull with a jump drive and the Ansiblex, sorted by name.
func (s *SDE) JumpShips() []JumpShip {
	return append([]JumpShip(nil), s.ensureJumpIndex().ships...)
}

// ResolveJumpShip finds a jump ship by name (trimmed, case-insensitive); "jump bridge" and "ansiblex" mean the Ansiblex.
func (s *SDE) ResolveJumpShip(name string) (JumpShip, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return JumpShip{}, false
	}
	ships := s.ensureJumpIndex().ships
	for _, sh := range ships {
		if strings.ToLower(sh.Name) == n {
			return sh, true
		}
	}
	if n == "jump bridge" || n == "ansiblex" {
		for _, sh := range ships {
			if sh.Ansiblex {
				return sh, true
			}
		}
	}
	return JumpShip{}, false
}

// JumpRangeLY is the jump range of the hull at the given skills; the Ansiblex's is fixed.
func (s *SDE) JumpRangeLY(sh JumpShip, sk JumpSkills) float64 {
	if sh.Ansiblex {
		return sh.BaseRangeLY
	}
	return sh.BaseRangeLY * (1 + s.ensureJumpIndex().calib/100*float64(sk.Calibration))
}

// JumpFuelPerLY is the fuel burned per light-year (before Jump Drive Economizers); 0 for the Ansiblex.
func (s *SDE) JumpFuelPerLY(sh JumpShip, sk JumpSkills) float64 {
	if sh.Ansiblex {
		return 0
	}
	return sh.BaseFuelPerLY *
		(1 + s.ensureJumpIndex().fuelSave/100*float64(sk.FuelConservation)) *
		(1 + sh.JumpFreighterBonus/100*float64(sk.JumpFreighters))
}

// DistanceLY is the straight-line distance between two systems in light-years.
func (s *SDE) DistanceLY(a, b int) (float64, bool) {
	idx := s.ensureJumpIndex()
	pa, ok1 := idx.pos[a]
	pb, ok2 := idx.pos[b]
	if !ok1 || !ok2 {
		return 0, false
	}
	return pa.lyTo(pb), true
}

func (p jumpPos) lyTo(q jumpPos) float64 {
	dx, dy, dz := p.x-q.x, p.y-q.y, p.z-q.z
	return math.Sqrt(dx*dx+dy*dy+dz*dz) / LightYear
}

// isWormholeSpace: classes 1–6 and 12–18 cannot use jump drives.
func isWormholeSpace(class int) bool {
	return (class >= 1 && class <= 6) || (class >= 12 && class <= 18)
}

// jumpPlace classifies a system as a hop end. Order: Zarzakh, Pochven, off-network (wormhole space or
// not), Ansiblex sovereignty, highsec.
func jumpPlace(idx *jumpIndex, f *SystemFacts, ansiblex bool) JumpPlace {
	switch {
	case f.ID == zarzakhSystemID:
		return JumpZarzakh
	case f.WormholeClass == pochvenClass:
		return JumpPochven
	case !idx.network[f.ID]:
		if isWormholeSpace(f.WormholeClass) {
			return JumpWormhole
		}
		return JumpOffNetwork
	}
	d := DisplaySecurity(f.Security)
	if ansiblex {
		if d > 0 || f.FactionID != 0 {
			return JumpNoSovereignty
		}
		return JumpOK
	}
	if d >= highsecDisplayThreshold {
		return JumpHighsec
	}
	return JumpOK
}

// JumpTarget says whether a hop of this ship can end in the system.
func (s *SDE) JumpTarget(id int, sh JumpShip) JumpPlace {
	idx := s.ensureJumpIndex()
	f := idx.facts.byID[id]
	if f == nil {
		return JumpOffNetwork
	}
	return jumpPlace(idx, f, sh.Ansiblex)
}

// JumpOrigin says whether a route of this ship can start in the system. Zarzakh and Pochven can start a
// route (not for an Ansiblex); highsec is reported only for a hull that may not be there.
func (s *SDE) JumpOrigin(id int, sh JumpShip) JumpPlace {
	idx := s.ensureJumpIndex()
	f := idx.facts.byID[id]
	if f == nil {
		return JumpOffNetwork
	}
	p := jumpPlace(idx, f, sh.Ansiblex)
	switch p {
	case JumpZarzakh, JumpPochven:
		if sh.Ansiblex {
			return JumpNoSovereignty
		}
		return JumpOK
	case JumpHighsec:
		if sh.NoHighsec {
			return JumpHighsec
		}
		return JumpOK
	}
	return p
}

// SystemsInJumpRange maps every system that is JumpTarget-OK for sh within rangeLY of origin to its
// distance in light-years; origin is never in the map.
func (s *SDE) SystemsInJumpRange(origin int, sh JumpShip, rangeLY float64) map[int]float64 {
	idx := s.ensureJumpIndex()
	out := map[int]float64{}
	po, ok := idx.pos[origin]
	if !ok {
		return out
	}
	targets := idx.cyno
	if sh.Ansiblex {
		targets = idx.ansiblex
	}
	for _, id := range targets {
		if id == origin {
			continue
		}
		if d := po.lyTo(idx.pos[id]); d <= rangeLY {
			out[id] = d
		}
	}
	return out
}

// jumpCost orders routes: fewest jumps, then fewest hops ending without an NPC station, then light-years.
type jumpCost struct {
	jumps, bare int
	ly          float64
}

func (a jumpCost) less(b jumpCost) bool {
	if a.jumps != b.jumps {
		return a.jumps < b.jumps
	}
	if a.bare != b.bare {
		return a.bare < b.bare
	}
	return a.ly < b.ly
}

type jumpNode struct {
	id      int
	p       jumpPos
	canEnd  bool // a hop may end here (false only for a start that is not a target)
	station bool
	cell    [3]int
}

type jumpItem struct {
	cost jumpCost
	node int
	id   int
}

type jumpHeap []jumpItem

func (h jumpHeap) Len() int { return len(h) }
func (h jumpHeap) Less(i, j int) bool {
	if h[i].cost != h[j].cost {
		return h[i].cost.less(h[j].cost)
	}
	return h[i].id < h[j].id
}
func (h jumpHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *jumpHeap) Push(x any)   { *h = append(*h, x.(jumpItem)) }
func (h *jumpHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// PlanJumpRoute plans origin → stops[0] → … → stops[len-1]; the caller checks every stop with
// JumpTarget first. ok is false when a leg has no route; failedLeg is its index (0 = origin → stops[0]).
//
// Each leg is a lexicographic Dijkstra on (jumps, hops ending without an NPC station when PreferStations,
// light-years) over the hop ends minus the avoided systems and regions; the stops themselves are exempt.
// Neighbours come from a uniform grid over the hop ends with one cell per jump range.
func (s *SDE) PlanJumpRoute(origin int, stops []int, sh JumpShip, o JumpRouteOptions) (steps []JumpStep, failedLeg int, ok bool) {
	idx := s.ensureJumpIndex()
	if len(stops) == 0 {
		return nil, 0, true
	}
	if o.RangeLY <= 0 {
		return nil, 0, false
	}
	exempt := map[int]bool{origin: true}
	for _, st := range stops {
		exempt[st] = true
	}
	hopEnds := idx.cyno
	if sh.Ansiblex {
		hopEnds = idx.ansiblex
	}
	isEnd := make(map[int]bool, len(hopEnds))
	var ids []int
	for _, id := range hopEnds {
		if !exempt[id] {
			if o.Avoid[id] {
				continue
			}
			if f := idx.facts.byID[id]; f != nil && o.AvoidRegions[f.RegionID] {
				continue
			}
		}
		isEnd[id] = true
		ids = append(ids, id)
	}
	for id := range exempt { // a start that is not a hop end still needs a position
		if !isEnd[id] {
			if _, has := idx.pos[id]; has {
				ids = append(ids, id)
			}
		}
	}
	sort.Ints(ids)

	cellSize := o.RangeLY * LightYear
	cellOf := func(p jumpPos) [3]int {
		return [3]int{int(math.Floor(p.x / cellSize)), int(math.Floor(p.y / cellSize)), int(math.Floor(p.z / cellSize))}
	}
	nodes := make([]jumpNode, len(ids))
	nodeOf := make(map[int]int, len(ids))
	grid := map[[3]int][]int{}
	for i, id := range ids {
		p := idx.pos[id]
		n := jumpNode{id: id, p: p, canEnd: isEnd[id], cell: cellOf(p)}
		if f := idx.facts.byID[id]; f != nil {
			n.station = f.NPCStations > 0
		}
		nodes[i] = n
		nodeOf[id] = i
		if n.canEnd {
			grid[n.cell] = append(grid[n.cell], i) // ascending node index = ascending system ID
		}
	}

	from := origin
	for leg, to := range stops {
		if to == from {
			continue
		}
		path, found := planJumpLeg(nodes, nodeOf, grid, from, to, o)
		if !found {
			return nil, leg, false
		}
		steps = append(steps, path...)
		from = to
	}
	return steps, 0, true
}

func planJumpLeg(nodes []jumpNode, nodeOf map[int]int, grid map[[3]int][]int, from, to int, o JumpRouteOptions) ([]JumpStep, bool) {
	src, okS := nodeOf[from]
	dst, okD := nodeOf[to]
	if !okS || !okD || !nodes[dst].canEnd {
		return nil, false
	}
	best := make(map[int]jumpCost)
	prev := make(map[int]int)
	start := jumpCost{}
	best[src] = start
	pq := &jumpHeap{{cost: start, node: src, id: from}}
	for pq.Len() > 0 {
		it := heap.Pop(pq).(jumpItem)
		if c, ok := best[it.node]; !ok || c != it.cost {
			continue // stale entry
		}
		if it.node == dst {
			break
		}
		u := nodes[it.node]
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for dz := -1; dz <= 1; dz++ {
					for _, vi := range grid[[3]int{u.cell[0] + dx, u.cell[1] + dy, u.cell[2] + dz}] {
						if vi == it.node {
							continue
						}
						v := nodes[vi]
						d := u.p.lyTo(v.p)
						if d > o.RangeLY {
							continue
						}
						bare := 0
						if o.PreferStations && !v.station {
							bare = 1
						}
						nc := jumpCost{it.cost.jumps + 1, it.cost.bare + bare, it.cost.ly + d}
						if c, seen := best[vi]; !seen || nc.less(c) {
							best[vi] = nc
							prev[vi] = it.node
							heap.Push(pq, jumpItem{cost: nc, node: vi, id: v.id})
						}
					}
				}
			}
		}
	}
	if _, ok := best[dst]; !ok {
		return nil, false
	}
	var rev []JumpStep
	for cur := dst; cur != src; {
		p := prev[cur]
		rev = append(rev, JumpStep{From: nodes[p].id, To: nodes[cur].id, LY: nodes[p].p.lyTo(nodes[cur].p)})
		cur = p
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, true
}
