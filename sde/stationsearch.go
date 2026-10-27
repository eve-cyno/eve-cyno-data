package sde

import (
	"database/sql"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// AgentFilter selects mission agents; zero values mean "any".
type AgentFilter struct {
	// Level is the agent level, 1 to 5; 0 = any.
	Level int
	// DivisionID is the NPC division (24 Security, 22 Distribution, 23 Mining, 18 R&D); 0 = any.
	DivisionID int
	// TypeIDs are agtAgentTypes ids; an agent must have one of them; nil = any.
	TypeIDs []int
	// CorporationID keeps one corporation's agents; 0 = any.
	CorporationID int
	// FactionID keeps the agents of one faction's corporations; 0 = any.
	FactionID int
	// LocatorOnly keeps only agents that offer the locator service.
	LocatorOnly bool
}

// Agent is one agtAgents row with its names.
type Agent struct {
	ID, Level, DivisionID, TypeID, CorporationID, StationID int
	// Name is the agent's name (invNames).
	Name string
	// Division is the NPC division name (crpNPCDivisions).
	Division string
	// Type is the agent type name (agtAgentTypes), for example "BasicAgent".
	Type string
	// Corporation is the agent's corporation name.
	Corporation string
	// Locator is true when the agent offers the locator service.
	Locator bool
}

// StationFilter selects NPC stations; zero values mean "any".
type StationFilter struct {
	// SystemID keeps one system's stations; 0 = any.
	SystemID int
	// RegionID keeps one region's stations; 0 = any.
	RegionID int
	// Near ranks by gate jumps from an origin and leaves out stations in systems without a gate
	// route; MaxJumps 0 = no radius. nil = no ranking.
	Near *NearFilter
	// Services are canonical staServices names; a station must offer all of them.
	Services []string
	// Agent keeps stations that have at least one matching agent; nil = no agent condition.
	Agent *AgentFilter
}

// StationMatch is one FindStations row.
type StationMatch struct {
	StationID int
	Name      string
	SystemID  int
	System    string
	// Security is the raw true security of the system; texts print DisplaySecurity.
	Security float64
	RegionID int
	Region   string
	OwnerID  int
	// Owner is the owning NPC corporation.
	Owner string
	// Services are the services of the station's operation, sorted.
	Services []string
	// ReprocessingEfficiency is the station's base refining yield, a fraction (0.5 = 50%).
	ReprocessingEfficiency float64
	// Jumps is the gate distance from Near.Origin, -1 without Near.
	Jumps int
	// Agents are the matching agents in agentID order; nil without an Agent filter.
	Agents []Agent
}

// ResolveNPCCorporationName case-insensitively maps an NPC corporation name to its ID.
func (s *SDE) ResolveNPCCorporationName(name string) *int {
	return s.resolveByName("crpNPCCorporations", "corporationID", "corporationName", name)
}

// NPCCorporationName returns the name of an NPC corporation ("" when unknown).
func (s *SDE) NPCCorporationName(id int) string {
	var name sql.NullString
	if err := s.db.QueryRow("SELECT corporationName FROM crpNPCCorporations WHERE corporationID = ?", id).Scan(&name); err != nil {
		return ""
	}
	return name.String
}

// FindStations returns every NPC station matching f: by jumps (with Near), then system name,
// then station name.
func (s *SDE) FindStations(f StationFilter) []StationMatch {
	if !s.HasTable("staStations") {
		return nil
	}
	idx := s.ensureSystemIndex()
	h, err := s.db.acquire()
	if err != nil {
		return nil
	}
	defer h.release()
	db := h.db
	var dist, hsDist map[int]int
	if f.Near != nil {
		dist = s.JumpDistances(f.Near.Origin, false, nil)
		if f.Near.PreferHighSec {
			hsDist = s.JumpDistances(f.Near.Origin, true, nil)
		}
	}

	corps, corpFaction := npcCorporations(db)
	opServices := operationServices(db)
	var agentsByStation map[int][]Agent
	if f.Agent != nil {
		agentsByStation = matchingAgents(db, *f.Agent, corps, corpFaction)
		if len(agentsByStation) == 0 {
			return []StationMatch{}
		}
	}

	query := "SELECT stationID, solarSystemID, corporationID, stationName, operationID, reprocessingEfficiency FROM staStations"
	var args []any
	if f.SystemID != 0 {
		query += " WHERE solarSystemID = ?"
		args = append(args, f.SystemID)
	}
	out := []StationMatch{}
	rows, err := db.Query(query, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, sys int
		var corp, op sql.NullInt64
		var name, eff sql.NullString
		if err := rows.Scan(&id, &sys, &corp, &name, &op, &eff); err != nil {
			continue
		}
		sf := idx.byID[sys]
		if sf == nil || (f.RegionID != 0 && sf.RegionID != f.RegionID) {
			continue
		}
		m := StationMatch{
			StationID: id, Name: name.String, SystemID: sys, System: sf.Name, Security: sf.Security,
			RegionID: sf.RegionID, Region: sf.Region, OwnerID: int(corp.Int64), Owner: corps[int(corp.Int64)],
			Services: opServices[int(op.Int64)], Jumps: -1,
		}
		if f.Near != nil {
			d, ok := dist[sys]
			if hd, hok := hsDist[sys]; hok {
				d, ok = hd, true
			}
			if !ok || (f.Near.MaxJumps > 0 && d > f.Near.MaxJumps) {
				continue
			}
			m.Jumps = d
		}
		if !hasAll(m.Services, f.Services) {
			continue
		}
		if f.Agent != nil {
			m.Agents = agentsByStation[id]
			if len(m.Agents) == 0 {
				continue
			}
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(eff.String), 64); err == nil {
			m.ReprocessingEfficiency = v
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Jumps != b.Jumps {
			return a.Jumps < b.Jumps
		}
		if a.System != b.System {
			return a.System < b.System
		}
		return a.Name < b.Name
	})
	return out
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// npcCorporations reads the NPC corporation names and their factions.
func npcCorporations(db *sql.DB) (names map[int]string, faction map[int]int) {
	names = idNames(db, "SELECT corporationID, corporationName FROM crpNPCCorporations")
	faction = idInts(db, "SELECT corporationID, factionID FROM crpNPCCorporations")
	return names, faction
}

// operationServices maps an operation to the sorted names of its services.
func operationServices(db *sql.DB) map[int][]string {
	svcNames := idNames(db, "SELECT serviceID, serviceName FROM staServices")
	out := map[int][]string{}
	eachRow(db, "operation services", "SELECT operationID, serviceID FROM staOperationServices", func(r *sql.Rows) error {
		var op, sv int
		if err := r.Scan(&op, &sv); err != nil {
			return err
		}
		if n := svcNames[sv]; n != "" {
			out[op] = append(out[op], n)
		}
		return nil
	})
	for _, list := range out {
		sort.Strings(list)
	}
	return out
}

// matchingAgents reads the agents that pass f, grouped by station and in agentID order. The
// agtAgents columns are TEXT in the generic table, so they are cast in Go.
func matchingAgents(db *sql.DB, f AgentFilter, corps map[int]string, corpFaction map[int]int) map[int][]Agent {
	names := map[int]string{}
	eachRow(db, "agent names", "SELECT a.agentID, n.itemName FROM agtAgents a JOIN invNames n ON n.itemID = a.agentID", func(r *sql.Rows) error {
		var id, name sql.NullString
		if err := r.Scan(&id, &name); err != nil {
			return err
		}
		names[textInt(id.String)] = name.String
		return nil
	})
	divisions := idNames(db, "SELECT CAST(divisionID AS INTEGER), divisionName FROM crpNPCDivisions")
	types := idNames(db, "SELECT CAST(agentTypeID AS INTEGER), agentType FROM agtAgentTypes")

	var all []Agent
	eachRow(db, "agents", "SELECT agentID, divisionID, corporationID, locationID, level, agentTypeID, isLocator FROM agtAgents", func(r *sql.Rows) error {
		var id, div, corp, loc, lvl, typ, locator sql.NullString
		if err := r.Scan(&id, &div, &corp, &loc, &lvl, &typ, &locator); err != nil {
			return err
		}
		a := Agent{
			ID: textInt(id.String), DivisionID: textInt(div.String), CorporationID: textInt(corp.String),
			StationID: textInt(loc.String), Level: textInt(lvl.String), TypeID: textInt(typ.String),
			Locator: textInt(locator.String) != 0,
		}
		switch {
		case f.Level != 0 && a.Level != f.Level,
			f.DivisionID != 0 && a.DivisionID != f.DivisionID,
			f.TypeIDs != nil && !slices.Contains(f.TypeIDs, a.TypeID),
			f.CorporationID != 0 && a.CorporationID != f.CorporationID,
			f.FactionID != 0 && corpFaction[a.CorporationID] != f.FactionID,
			f.LocatorOnly && !a.Locator:
			return nil
		}
		a.Name, a.Division, a.Type, a.Corporation = names[a.ID], divisions[a.DivisionID], types[a.TypeID], corps[a.CorporationID]
		all = append(all, a)
		return nil
	})
	sort.SliceStable(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	out := map[int][]Agent{}
	for _, a := range all {
		out[a.StationID] = append(out[a.StationID], a)
	}
	return out
}
