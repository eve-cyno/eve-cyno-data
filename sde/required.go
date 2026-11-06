package sde

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
)

// RequiredColumns is the hard set: the SDE columns without which the dogma and fit
// engine, the item facts or the core readers answer wrong without noticing, per table.
// The readers swallow query errors (a missing column makes GetEffectModifiers return nil,
// GetAttributeMeta a zero value, GetDogma drop mass/volume/capacity, TypeBasics loses the
// type description, PortionSize the portion size and IsMarketTradable the market group,
// GetItemMetaTier reads every item as "Tech I", the jump index loses its positions
// (mapSolarSystems x/y/z), BlueprintOutputPerRun the units per run
// (industryActivityProducts.quantity)), so a schema that lacks one of these degrades
// silently: no skill/ship modifiers reach the dogma engine, no stacking penalties.
//
// Three checks consume it: the SDE builder's publish gate in sde/build (a build missing
// a column never replaces the live file), the refresh in sde/build (a live file missing
// one is rebuilt at an unchanged build) and the open-time check in openHandle (an
// already-deployed file missing one is reported at ERROR).
//
// Every curated table of sde/build/builder.go is here: the builder creates those columns
// itself, and a generic-phase failure cannot drop them. Of the generic tables (loaded as
// TEXT from the CSV header, a failed CSV only logged) only the meta tier is hard.
//
// Keep it in step with the CREATE TABLE statements in
// sde/build/builder.go and with the queries in effects.go, dogma.go, typebasics.go,
// industry.go, systems.go, jumps.go, typefacts.go, ships.go, modules.go and stationsearch.go.
var RequiredColumns = map[string][]string{
	"invTypes": {"typeID", "groupID", "typeName", "description", "published", "mass", "volume", "capacity", "portionSize", "marketGroupID", "metaLevel", "techLevel", "basePrice", "packagedVolume"},
	// The item facts (TypeFacts): group, category, meta group and market group path.
	"invGroups":     {"groupID", "groupName", "categoryID"},
	"invCategories": {"categoryID", "categoryName"},
	// The meta tier (generic tables): item facts, module search, the fit detail and the
	// desktop pack's meta groups. Without it GetItemMetaTier reads every item as "Tech I".
	"invMetaTypes":      {"typeID", "metaGroupID"},
	"invMetaGroups":     {"metaGroupID", "metaGroupName"},
	"invMarketGroups":   {"marketGroupID", "parentGroupID", "marketGroupName"},
	"dgmEffects":        {"effectID", "effectName", "effectCategory", "modifierInfo"},
	"dgmAttributeTypes": {"attributeID", "attributeName", "defaultValue", "stackable", "highIsGood"},
	// The jump planner: light-year distances.
	"mapSolarSystems": {"solarSystemID", "regionID", "security", "x", "y", "z"},
	// The production chain: the blueprint that makes a type, and the units one run
	// makes (BlueprintOutputPerRun; without quantity every sub-component job would
	// silently take one run per unit needed).
	"industryActivityProducts": {"blueprint_typeID", "activityID", "product_typeID", "quantity"},
	// The station search (FindStations).
	"staStations":        {"stationID", "solarSystemID", "corporationID", "stationName", "operationID", "reprocessingEfficiency"},
	"crpNPCCorporations": {"corporationID", "corporationName", "factionID"},
}

// FeatureColumns are the generic-phase columns one tool answer needs, per table. A build
// without one still publishes (the gate logs it at ERROR), a live file without one is not
// rebuilt at an unchanged build, and the tool that needs it answers "unavailable" (see
// FeatureGap) instead of answering from missing data. Keep it in step with the queries in
// industry.go, systems.go, stationsearch.go and planetary.go, and with the FeatureGap
// calls in tools.
var FeatureColumns = map[string][]string{
	// The Alpha clone skill caps (AlphaCloneSkills): its error makes validate_fitting skip
	// the Alpha check and say so.
	"chrCloneGradeSkills": {"typeID", "level"},
	// The system index (find_systems, get_system_info): celestial counts and wormhole classes.
	"mapDenormalize":             {"solarSystemID", "groupID", "typeID"},
	"mapLocationWormholeClasses": {"locationID", "wormholeClassID"},
	// The mission agents of the station search (get_npc_stations with agent).
	"agtAgents":       {"agentID", "divisionID", "corporationID", "locationID", "level", "agentTypeID", "isLocator"},
	"agtAgentTypes":   {"agentTypeID", "agentType"},
	"crpNPCDivisions": {"divisionID", "divisionName"},
	"invNames":        {"itemID", "itemName"},
	// The planetary production chains (SchematicFor).
	"planetSchematics":        {"schematicID", "schematicName", "cycleTime"},
	"planetSchematicsTypeMap": {"schematicID", "typeID", "quantity", "isInput"},
}

// The tables each tool's FeatureGap check covers.
var (
	SystemFeatureTables    = []string{"mapDenormalize", "mapLocationWormholeClasses"}
	AgentFeatureTables     = []string{"agtAgents", "agtAgentTypes", "crpNPCDivisions", "invNames"}
	PlanetaryFeatureTables = []string{"planetSchematics", "planetSchematicsTypeMap"}
)

// MissingRequiredColumns returns every "table.column" from RequiredColumns that db
// lacks, sorted. A table that does not exist reports all of its columns.
func MissingRequiredColumns(db *sql.DB) ([]string, error) {
	return missingColumns(db, RequiredColumns, nil)
}

// MissingFeatureColumns is MissingRequiredColumns for FeatureColumns.
func MissingFeatureColumns(db *sql.DB) ([]string, error) {
	return missingColumns(db, FeatureColumns, nil)
}

// FeatureGap returns the FeatureColumns of tables that the file being served lacks, as
// sorted "table.column" entries. It returns nil when nothing is missing, and also when the
// check itself fails or the SDE is not open: callers check Available first.
func (s *SDE) FeatureGap(tables ...string) []string {
	if s == nil || s.db == nil {
		return nil
	}
	h, err := s.db.acquire()
	if err != nil {
		return nil
	}
	defer h.release()
	missing, err := missingColumns(h.db, FeatureColumns, tables)
	if err != nil {
		slog.Warn("sde: feature-column check failed", "tables", tables, "error", err)
		return nil
	}
	return missing
}

// missingColumns returns the "table.column" entries of spec that db lacks, sorted; only
// the tables listed in only, or every table of spec when only is empty.
func missingColumns(db *sql.DB, spec map[string][]string, only []string) ([]string, error) {
	tables := only
	if len(tables) == 0 {
		for t := range spec {
			tables = append(tables, t)
		}
	}
	var missing []string
	for _, table := range tables {
		cols := spec[table]
		if len(cols) == 0 {
			continue
		}
		have, err := tableColumns(db, table)
		if err != nil {
			return nil, err
		}
		for _, c := range cols {
			if !have[c] {
				missing = append(missing, table+"."+c)
			}
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// tableColumns returns the set of column names of table (empty when it does not exist).
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	// table_info is a table-valued pragma function: the name is bound, not interpolated.
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("sde: table_info %q: %w", table, err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sde: table_info %q: %w", table, err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sde: table_info %q: %w", table, err)
	}
	return have, nil
}

// logMissingRequiredColumns reports a freshly opened SDE file that lacks RequiredColumns
// (at ERROR) or FeatureColumns (at WARN). It never fails the open: the file still serves
// everything else.
func logMissingRequiredColumns(db *sql.DB, path string) {
	missing, err := MissingRequiredColumns(db)
	if err != nil {
		slog.Warn("sde: required-column check failed", "path", path, "error", err)
		return
	}
	if len(missing) > 0 {
		slog.Error("sde: file lacks columns the readers need; dogma modifiers, stacking, mass/volume/capacity, type descriptions, meta tiers and market groups, the jump readers and production-chain runs degrade silently - rebuild it (go run ./cmd/sde -force)",
			"path", path, "missing", missing)
	}
	feature, err := MissingFeatureColumns(db)
	if err != nil {
		slog.Warn("sde: feature-column check failed", "path", path, "error", err)
		return
	}
	if len(feature) > 0 {
		slog.Warn("sde: file lacks feature columns; the tools that need them answer unavailable until a rebuild (go run ./cmd/sde -force)",
			"path", path, "missing", feature)
	}
}
