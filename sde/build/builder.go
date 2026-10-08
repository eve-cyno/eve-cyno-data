// core/sde/build/builder.go
//
// BuildSQLite constructs a fresh SDE SQLite database from Fuzzwork CSV dumps.
// Layout:
//   - Phase 1 (curated): 25 explicit typed schemas + indexes.
//   - Phase 2 (generic): every other CSV with TEXT columns from the header, no
//     indexes (except the post-phase-2 generic-table indexes below).
//
// Memory safety: every CSV is read row-by-row via encoding/csv and committed in
// batches of 1 000 rows. The full dump is never loaded into a Go slice.
package build

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	coresde "eve-cyno.dev/go/data/sde"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// batchSize is the number of rows committed per transaction. 1 000 rows keeps
// each transaction small while still being much faster than one-per-row.
const batchSize = 1000

// curatedTable describes one of the 25 explicit typed schemas.
type curatedTable struct {
	name    string
	create  string
	insert  string
	columns []string // subset of CSV columns to read (in CSV order)
	extract func(row map[string]string) ([]any, bool)
}

// safeInt converts an empty/None-ish string to nil, otherwise parses to int64.
// Returns (nil, true) on empty/None so the caller can store NULL.
func safeInt(v string) *int64 {
	v = strings.TrimSpace(v)
	if v == "" || v == "None" {
		return nil
	}
	// Handle floats like "12.0"
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		i := int64(f)
		return &i
	}
	return nil
}

// safeFloat converts a string to *float64, returning nil for empty/None.
func safeFloat(v string) *float64 {
	v = strings.TrimSpace(v)
	if v == "" || v == "None" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

// mustInt parses an integer, returning 0 on any error (used for non-nullable PK columns).
func mustInt(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return int64(f)
	}
	return 0
}

// orNil returns nil if s is empty, otherwise returns s as interface{}.
func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// curatedTables is the authoritative list of 25 curated schemas.
// The columns the readers in core/sde depend on are listed in sde.RequiredColumns.
var curatedTables = []curatedTable{
	{
		name: "invTypes",
		// mass/volume/capacity are type-record columns that are also dogma
		// attributes 4/161/38; core/sde GetDogma merges them in (see RequiredColumns).
		create: `CREATE TABLE IF NOT EXISTS invTypes (
			typeID    INTEGER PRIMARY KEY,
			groupID   INTEGER,
			typeName  TEXT,
			published INTEGER,
			mass      REAL,
			volume    REAL,
			capacity  REAL
		)`,
		insert:  "INSERT OR REPLACE INTO invTypes VALUES (?, ?, ?, ?, ?, ?, ?)",
		columns: []string{"typeID", "groupID", "typeName", "published", "mass", "volume", "capacity"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["typeID"]), safeInt(r["groupID"]), orNil(r["typeName"]), mustInt(r["published"]),
				safeFloat(r["mass"]), safeFloat(r["volume"]), safeFloat(r["capacity"])}, true
		},
	},
	{
		name: "dgmTypeAttributes",
		create: `CREATE TABLE IF NOT EXISTS dgmTypeAttributes (
			typeID      INTEGER,
			attributeID INTEGER,
			valueInt    REAL,
			valueFloat  REAL,
			PRIMARY KEY (typeID, attributeID)
		)`,
		insert:  "INSERT OR REPLACE INTO dgmTypeAttributes VALUES (?, ?, ?, ?)",
		columns: []string{"typeID", "attributeID", "valueInt", "valueFloat"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["typeID"]), mustInt(r["attributeID"]), safeFloat(r["valueInt"]), safeFloat(r["valueFloat"])}, true
		},
	},
	{
		name: "dgmTypeEffects",
		create: `CREATE TABLE IF NOT EXISTS dgmTypeEffects (
			typeID    INTEGER,
			effectID  INTEGER,
			isDefault INTEGER,
			PRIMARY KEY (typeID, effectID)
		)`,
		insert:  "INSERT OR REPLACE INTO dgmTypeEffects VALUES (?, ?, ?)",
		columns: []string{"typeID", "effectID", "isDefault"},
		extract: func(r map[string]string) ([]any, bool) {
			def := safeInt(r["isDefault"])
			var defVal int64
			if def != nil {
				defVal = *def
			}
			return []any{mustInt(r["typeID"]), mustInt(r["effectID"]), defVal}, true
		},
	},
	{
		name: "dgmEffects",
		// modifierInfo is the dogma modifier list as a JSON array, stored verbatim
		// from Fuzzwork (core/sde GetEffectModifiers parses it).
		create: `CREATE TABLE IF NOT EXISTS dgmEffects (
			effectID       INTEGER PRIMARY KEY,
			effectName     TEXT,
			effectCategory INTEGER,
			displayName    TEXT,
			published      INTEGER,
			modifierInfo   TEXT
		)`,
		insert:  "INSERT OR REPLACE INTO dgmEffects VALUES (?, ?, ?, ?, ?, ?)",
		columns: []string{"effectID", "effectName", "effectCategory", "displayName", "published", "modifierInfo"},
		extract: func(r map[string]string) ([]any, bool) {
			def := safeInt(r["published"])
			var pub int64
			if def != nil {
				pub = *def
			}
			return []any{mustInt(r["effectID"]), orNil(r["effectName"]), safeInt(r["effectCategory"]), orNil(r["displayName"]), pub,
				orNil(r["modifierInfo"])}, true
		},
	},
	{
		name: "dgmAttributeTypes",
		// defaultValue/stackable/highIsGood drive stacking penalties and attribute
		// defaults in the dogma engine (core/sde GetAttributeMeta).
		create: `CREATE TABLE IF NOT EXISTS dgmAttributeTypes (
			attributeID    INTEGER PRIMARY KEY,
			attributeName  TEXT,
			displayName    TEXT,
			unitID         INTEGER,
			published      INTEGER,
			defaultValue   REAL,
			stackable      INTEGER,
			highIsGood     INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO dgmAttributeTypes VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		columns: []string{"attributeID", "attributeName", "displayName", "unitID", "published", "defaultValue", "stackable", "highIsGood"},
		extract: func(r map[string]string) ([]any, bool) {
			def := safeInt(r["published"])
			var pub int64
			if def != nil {
				pub = *def
			}
			return []any{mustInt(r["attributeID"]), orNil(r["attributeName"]), orNil(r["displayName"]), safeInt(r["unitID"]), pub,
				safeFloat(r["defaultValue"]), safeInt(r["stackable"]), safeInt(r["highIsGood"])}, true
		},
	},
	{
		name: "invGroups",
		create: `CREATE TABLE IF NOT EXISTS invGroups (
			groupID    INTEGER PRIMARY KEY,
			categoryID INTEGER,
			groupName  TEXT,
			published  INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO invGroups VALUES (?, ?, ?, ?)",
		columns: []string{"groupID", "categoryID", "groupName", "published"},
		extract: func(r map[string]string) ([]any, bool) {
			def := safeInt(r["published"])
			var pub int64
			if def != nil {
				pub = *def
			}
			return []any{mustInt(r["groupID"]), safeInt(r["categoryID"]), orNil(r["groupName"]), pub}, true
		},
	},
	{
		name: "invCategories",
		create: `CREATE TABLE IF NOT EXISTS invCategories (
			categoryID   INTEGER PRIMARY KEY,
			categoryName TEXT,
			published    INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO invCategories VALUES (?, ?, ?)",
		columns: []string{"categoryID", "categoryName", "published"},
		extract: func(r map[string]string) ([]any, bool) {
			def := safeInt(r["published"])
			var pub int64
			if def != nil {
				pub = *def
			}
			return []any{mustInt(r["categoryID"]), orNil(r["categoryName"]), pub}, true
		},
	},
	{
		name: "eveUnits",
		create: `CREATE TABLE IF NOT EXISTS eveUnits (
			unitID      INTEGER PRIMARY KEY,
			unitName    TEXT,
			displayName TEXT,
			description TEXT
		)`,
		insert:  "INSERT OR REPLACE INTO eveUnits VALUES (?, ?, ?, ?)",
		columns: []string{"unitID", "unitName", "displayName", "description"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["unitID"]), orNil(r["unitName"]), orNil(r["displayName"]), orNil(r["description"])}, true
		},
	},
	{
		name: "chrFactions",
		create: `CREATE TABLE IF NOT EXISTS chrFactions (
			factionID            INTEGER PRIMARY KEY,
			factionName          TEXT,
			solarSystemID        INTEGER,
			corporationID        INTEGER,
			militiaCorporationID INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO chrFactions VALUES (?, ?, ?, ?, ?)",
		columns: []string{"factionID", "factionName", "solarSystemID", "corporationID", "militiaCorporationID"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["factionID"]), orNil(r["factionName"]), safeInt(r["solarSystemID"]), safeInt(r["corporationID"]), safeInt(r["militiaCorporationID"])}, true
		},
	},
	{
		name: "chrRaces",
		create: `CREATE TABLE IF NOT EXISTS chrRaces (
			raceID   INTEGER PRIMARY KEY,
			raceName TEXT,
			shortDescription TEXT
		)`,
		insert:  "INSERT OR REPLACE INTO chrRaces VALUES (?, ?, ?)",
		columns: []string{"raceID", "raceName", "shortDescription"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["raceID"]), orNil(r["raceName"]), orNil(r["shortDescription"])}, true
		},
	},
	{
		name: "industryActivity",
		create: `CREATE TABLE IF NOT EXISTS industryActivity (
			blueprint_typeID INTEGER,
			activityID       INTEGER,
			time             INTEGER,
			PRIMARY KEY (blueprint_typeID, activityID)
		)`,
		insert:  "INSERT OR REPLACE INTO industryActivity VALUES (?, ?, ?)",
		columns: []string{"typeID", "activityID", "time"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["typeID"]), mustInt(r["activityID"]), safeInt(r["time"])}, true
		},
	},
	{
		name: "industryActivityMaterials",
		create: `CREATE TABLE IF NOT EXISTS industryActivityMaterials (
			blueprint_typeID INTEGER,
			activityID       INTEGER,
			material_typeID  INTEGER,
			quantity         INTEGER,
			PRIMARY KEY (blueprint_typeID, activityID, material_typeID)
		)`,
		insert:  "INSERT OR REPLACE INTO industryActivityMaterials VALUES (?, ?, ?, ?)",
		columns: []string{"typeID", "activityID", "materialTypeID", "quantity"},
		extract: func(r map[string]string) ([]any, bool) {
			qty := safeInt(r["quantity"])
			var q int64
			if qty != nil {
				q = *qty
			}
			return []any{mustInt(r["typeID"]), mustInt(r["activityID"]), mustInt(r["materialTypeID"]), q}, true
		},
	},
	{
		name: "industryActivityProducts",
		create: `CREATE TABLE IF NOT EXISTS industryActivityProducts (
			blueprint_typeID INTEGER,
			activityID       INTEGER,
			product_typeID   INTEGER,
			quantity         INTEGER,
			probability      REAL,
			PRIMARY KEY (blueprint_typeID, activityID, product_typeID)
		)`,
		insert:  "INSERT OR REPLACE INTO industryActivityProducts VALUES (?, ?, ?, ?, ?)",
		columns: []string{"typeID", "activityID", "productTypeID", "quantity", "probability"},
		extract: func(r map[string]string) ([]any, bool) {
			qty := safeInt(r["quantity"])
			var q int64
			if qty != nil {
				q = *qty
			}
			return []any{mustInt(r["typeID"]), mustInt(r["activityID"]), mustInt(r["productTypeID"]), q, safeFloat(r["probability"])}, true
		},
	},
	{
		name: "industryActivitySkills",
		create: `CREATE TABLE IF NOT EXISTS industryActivitySkills (
			blueprint_typeID INTEGER,
			activityID       INTEGER,
			skill_typeID     INTEGER,
			level            INTEGER,
			PRIMARY KEY (blueprint_typeID, activityID, skill_typeID)
		)`,
		insert:  "INSERT OR REPLACE INTO industryActivitySkills VALUES (?, ?, ?, ?)",
		columns: []string{"typeID", "activityID", "skillID", "level"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["typeID"]), mustInt(r["activityID"]), mustInt(r["skillID"]), mustInt(r["level"])}, true
		},
	},
	{
		name: "invTypeMaterials",
		create: `CREATE TABLE IF NOT EXISTS invTypeMaterials (
			typeID         INTEGER,
			materialTypeID INTEGER,
			quantity       INTEGER,
			PRIMARY KEY (typeID, materialTypeID)
		)`,
		insert:  "INSERT OR REPLACE INTO invTypeMaterials VALUES (?, ?, ?)",
		columns: []string{"typeID", "materialTypeID", "quantity"},
		extract: func(r map[string]string) ([]any, bool) {
			qty := safeInt(r["quantity"])
			var q int64
			if qty != nil {
				q = *qty
			}
			return []any{mustInt(r["typeID"]), mustInt(r["materialTypeID"]), q}, true
		},
	},
	{
		name: "mapSolarSystems",
		create: `CREATE TABLE IF NOT EXISTS mapSolarSystems (
			solarSystemID    INTEGER PRIMARY KEY,
			regionID         INTEGER,
			constellationID  INTEGER,
			solarSystemName  TEXT,
			security         REAL,
			securityClass    TEXT,
			factionID        INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO mapSolarSystems VALUES (?, ?, ?, ?, ?, ?, ?)",
		columns: []string{"solarSystemID", "regionID", "constellationID", "solarSystemName", "security", "securityClass", "factionID"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["solarSystemID"]), safeInt(r["regionID"]), safeInt(r["constellationID"]), orNil(r["solarSystemName"]), safeFloat(r["security"]), orNil(r["securityClass"]), safeInt(r["factionID"])}, true
		},
	},
	{
		name: "mapSolarSystemJumps",
		create: `CREATE TABLE IF NOT EXISTS mapSolarSystemJumps (
			fromSolarSystemID INTEGER,
			toSolarSystemID   INTEGER,
			PRIMARY KEY (fromSolarSystemID, toSolarSystemID)
		)`,
		insert:  "INSERT OR REPLACE INTO mapSolarSystemJumps VALUES (?, ?)",
		columns: []string{"fromSolarSystemID", "toSolarSystemID"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["fromSolarSystemID"]), mustInt(r["toSolarSystemID"])}, true
		},
	},
	{
		name: "mapRegions",
		create: `CREATE TABLE IF NOT EXISTS mapRegions (
			regionID    INTEGER PRIMARY KEY,
			regionName  TEXT,
			factionID   INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO mapRegions VALUES (?, ?, ?)",
		columns: []string{"regionID", "regionName", "factionID"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["regionID"]), orNil(r["regionName"]), safeInt(r["factionID"])}, true
		},
	},
	{
		name: "mapConstellations",
		create: `CREATE TABLE IF NOT EXISTS mapConstellations (
			constellationID    INTEGER PRIMARY KEY,
			regionID           INTEGER,
			constellationName  TEXT,
			factionID          INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO mapConstellations VALUES (?, ?, ?, ?)",
		columns: []string{"constellationID", "regionID", "constellationName", "factionID"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["constellationID"]), safeInt(r["regionID"]), orNil(r["constellationName"]), safeInt(r["factionID"])}, true
		},
	},
	{
		name: "staStations",
		create: `CREATE TABLE IF NOT EXISTS staStations (
			stationID        INTEGER PRIMARY KEY,
			solarSystemID    INTEGER,
			regionID         INTEGER,
			corporationID    INTEGER,
			stationTypeID    INTEGER,
			stationName      TEXT,
			operationID      INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO staStations VALUES (?, ?, ?, ?, ?, ?, ?)",
		columns: []string{"stationID", "solarSystemID", "regionID", "corporationID", "stationTypeID", "stationName", "operationID"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["stationID"]), safeInt(r["solarSystemID"]), safeInt(r["regionID"]), safeInt(r["corporationID"]), safeInt(r["stationTypeID"]), orNil(r["stationName"]), safeInt(r["operationID"])}, true
		},
	},
	{
		name: "staOperationServices",
		create: `CREATE TABLE IF NOT EXISTS staOperationServices (
			operationID  INTEGER,
			serviceID    INTEGER,
			PRIMARY KEY (operationID, serviceID)
		)`,
		insert:  "INSERT OR REPLACE INTO staOperationServices VALUES (?, ?)",
		columns: []string{"operationID", "serviceID"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["operationID"]), mustInt(r["serviceID"])}, true
		},
	},
	{
		name: "staServices",
		create: `CREATE TABLE IF NOT EXISTS staServices (
			serviceID    INTEGER PRIMARY KEY,
			serviceName  TEXT,
			description  TEXT
		)`,
		insert:  "INSERT OR REPLACE INTO staServices VALUES (?, ?, ?)",
		columns: []string{"serviceID", "serviceName", "description"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["serviceID"]), orNil(r["serviceName"]), orNil(r["description"])}, true
		},
	},
	{
		name: "industryActivityProbabilities",
		create: `CREATE TABLE IF NOT EXISTS industryActivityProbabilities (
			blueprint_typeID INTEGER,
			activityID       INTEGER,
			product_typeID   INTEGER,
			probability      REAL,
			PRIMARY KEY (blueprint_typeID, activityID, product_typeID)
		)`,
		insert:  "INSERT OR REPLACE INTO industryActivityProbabilities VALUES (?, ?, ?, ?)",
		columns: []string{"typeID", "activityID", "productTypeID", "probability"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["typeID"]), mustInt(r["activityID"]), mustInt(r["productTypeID"]), safeFloat(r["probability"])}, true
		},
	},
	{
		name: "invMarketGroups",
		create: `CREATE TABLE IF NOT EXISTS invMarketGroups (
			marketGroupID   INTEGER PRIMARY KEY,
			parentGroupID   INTEGER,
			marketGroupName TEXT,
			hasTypes        INTEGER
		)`,
		insert:  "INSERT OR REPLACE INTO invMarketGroups VALUES (?, ?, ?, ?)",
		columns: []string{"marketGroupID", "parentGroupID", "marketGroupName", "hasTypes"},
		extract: func(r map[string]string) ([]any, bool) {
			ht := safeInt(r["hasTypes"])
			var htVal int64
			if ht != nil {
				htVal = *ht
			}
			return []any{mustInt(r["marketGroupID"]), safeInt(r["parentGroupID"]), orNil(r["marketGroupName"]), htVal}, true
		},
	},
	{
		name: "crpNPCCorporations",
		create: `CREATE TABLE IF NOT EXISTS crpNPCCorporations (
			corporationID INTEGER PRIMARY KEY,
			factionID     INTEGER,
			solarSystemID INTEGER,
			description   TEXT
		)`,
		insert:  "INSERT OR REPLACE INTO crpNPCCorporations VALUES (?, ?, ?, ?)",
		columns: []string{"corporationID", "factionID", "solarSystemID", "description"},
		extract: func(r map[string]string) ([]any, bool) {
			return []any{mustInt(r["corporationID"]), safeInt(r["factionID"]), safeInt(r["solarSystemID"]), orNil(r["description"])}, true
		},
	},
}

// curatedSet is the set of curated table names for fast lookup in Phase 2.
var curatedSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(curatedTables))
	for _, t := range curatedTables {
		m[t.name] = struct{}{}
	}
	return m
}()

// Client is the interface BuildSQLite uses to fetch the CSV tables, so tests can inject a
// fake. *Fuzzwork satisfies it.
type Client interface {
	ListTables(ctx context.Context) ([]string, error)
	DownloadTable(ctx context.Context, name string) (io.ReadCloser, error)
}

// Gate is the publish gate: the last check a freshly built SDE must pass before it
// replaces the live file. Every required column (core/sde RequiredColumns) must exist, and
// the columns the dogma readers depend on must actually carry data: a build whose
// CREATE TABLE is right but whose CSV extraction silently produced NULLs is just as
// useless as one missing the column. The zero Gate has no row-count floors (required
// columns are still checked); DefaultGate is the production one.
type Gate struct {
	minModifierInfoRows int // dgmEffects rows with a non-empty modifierInfo
	minMassRows         int // invTypes rows with mass > 0
}

// DefaultGate holds the production thresholds. A healthy Fuzzwork dump has
// ~3,200 effects with modifiers and ~21,000 types with mass; 1,000 leaves wide
// headroom for a legitimately smaller dump while still catching an empty column.
func DefaultGate() Gate {
	return Gate{minModifierInfoRows: 1000, minMassRows: 1000}
}

// check verifies db against the gate. The error lists every problem found.
func (g Gate) check(ctx context.Context, db *sql.DB) error {
	var problems []string

	missing, err := coresde.MissingRequiredColumns(db)
	if err != nil {
		return fmt.Errorf("check required columns: %w", err)
	}
	if len(missing) > 0 {
		problems = append(problems, "missing required columns: "+strings.Join(missing, ", "))
	}

	// The row-count probes only make sense for columns that exist.
	has := make(map[string]bool, len(missing))
	for _, m := range missing {
		has[m] = true
	}
	if !has["dgmEffects.modifierInfo"] {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM dgmEffects WHERE modifierInfo IS NOT NULL AND modifierInfo != ''`).Scan(&n); err != nil {
			return fmt.Errorf("count dgmEffects.modifierInfo: %w", err)
		}
		if n < g.minModifierInfoRows {
			problems = append(problems, fmt.Sprintf("dgmEffects rows with modifierInfo: %d < %d", n, g.minModifierInfoRows))
		}
	}
	if !has["invTypes.mass"] {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM invTypes WHERE mass > 0`).Scan(&n); err != nil {
			return fmt.Errorf("count invTypes.mass: %w", err)
		}
		if n < g.minMassRows {
			problems = append(problems, fmt.Sprintf("invTypes rows with mass > 0: %d < %d", n, g.minMassRows))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// BuildSQLite builds a fresh SDE SQLite database at dstPath.
//
// It writes to a temporary file "<dstPath>.tmp" and atomically renames it to
// dstPath on success, so a live database is never corrupted by a partial run.
// Before the rename the new build must pass the publish gate (DefaultGate);
// a build that fails it is discarded and the live file is left untouched. Every
// caller (the `ingest sde` command and the hot-reload ingest source, both through
// Builder.Refresh) goes through the gate, so it protects both.
//
// Phase 1: 25 curated tables with typed schemas.
// Phase 2: every remaining table with all-TEXT columns from the CSV header.
// Both phases stream CSV row-by-row; no full file is ever held in memory.
func BuildSQLite(ctx context.Context, dstPath string, fw Client) error {
	return buildSQLite(ctx, dstPath, fw, DefaultGate())
}

// buildSQLite is BuildSQLite with an explicit publish gate (tests use tiny fixtures).
func buildSQLite(ctx context.Context, dstPath string, fw Client, gate Gate) error {
	tmpPath := dstPath + ".tmp"

	// Ensure the destination directory exists.
	dir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("build sde sqlite: mkdir %q: %w", dir, err)
	}

	// Remove stale temp file from a previous run.
	_ = os.Remove(tmpPath)

	db, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return fmt.Errorf("build sde sqlite: open temp db: %w", err)
	}
	defer func() {
		db.Close()
		// Clean up temp file on any failure path.
		if _, err2 := os.Stat(tmpPath); err2 == nil {
			_ = os.Remove(tmpPath)
		}
	}()

	// Speed PRAGMAs — safe because this is a throwaway build file.
	for _, pragma := range []string{
		"PRAGMA journal_mode=OFF",
		"PRAGMA synchronous=OFF",
		"PRAGMA cache_size=-65536", // 64 MB page cache
		"PRAGMA temp_store=MEMORY",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("build sde sqlite: pragma: %w", err)
		}
	}

	slog.Info("sde build: pragmas set, loading curated tables", "curated", len(curatedTables))

	// Phase 1: curated tables.
	for _, tbl := range curatedTables {
		slog.Info("sde build: curated table", "name", tbl.name)
		if err := loadCurated(ctx, db, tbl, fw); err != nil {
			return fmt.Errorf("build sde sqlite: curated %q: %w", tbl.name, err)
		}
	}

	slog.Info("sde build: curated done, listing generic tables")

	// Phase 2: generic tables (remaining CSV files).
	allTables, err := fw.ListTables(ctx)
	if err != nil {
		return fmt.Errorf("build sde sqlite: list tables: %w", err)
	}
	slog.Info("sde build: generic phase", "total_tables", len(allTables))
	for _, name := range allTables {
		if _, isCurated := curatedSet[name]; isCurated {
			continue
		}
		if err := loadGeneric(ctx, db, name, fw); err != nil {
			// Non-fatal: log and continue.
			fmt.Printf("sde build: generic %q failed: %v (skipped)\n", name, err)
		}
	}

	// Curated indexes.
	curatedIndexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_invTypes_name ON invTypes (typeName)",
		"CREATE INDEX IF NOT EXISTS idx_invTypes_group ON invTypes (groupID)",
		"CREATE INDEX IF NOT EXISTS idx_dta_type ON dgmTypeAttributes (typeID)",
		"CREATE INDEX IF NOT EXISTS idx_iam_bp ON industryActivityMaterials (blueprint_typeID, activityID)",
		"CREATE INDEX IF NOT EXISTS idx_iap_product ON industryActivityProducts (product_typeID, activityID)",
		"CREATE INDEX IF NOT EXISTS idx_ias_bp ON industryActivitySkills (blueprint_typeID, activityID)",
		"CREATE INDEX IF NOT EXISTS idx_itm_type ON invTypeMaterials (typeID)",
		"CREATE INDEX IF NOT EXISTS idx_mss_name ON mapSolarSystems (solarSystemName)",
		"CREATE INDEX IF NOT EXISTS idx_mss_region ON mapSolarSystems (regionID)",
		"CREATE INDEX IF NOT EXISTS idx_mss_security ON mapSolarSystems (security)",
		"CREATE INDEX IF NOT EXISTS idx_mssj_from ON mapSolarSystemJumps (fromSolarSystemID)",
		"CREATE INDEX IF NOT EXISTS idx_mssj_to ON mapSolarSystemJumps (toSolarSystemID)",
		"CREATE INDEX IF NOT EXISTS idx_mr_name ON mapRegions (regionName)",
		"CREATE INDEX IF NOT EXISTS idx_mc_name ON mapConstellations (constellationName)",
		"CREATE INDEX IF NOT EXISTS idx_sts_system ON staStations (solarSystemID)",
		"CREATE INDEX IF NOT EXISTS idx_sts_corp ON staStations (corporationID)",
		"CREATE INDEX IF NOT EXISTS idx_sts_op ON staStations (operationID)",
		"CREATE INDEX IF NOT EXISTS idx_dte_type ON dgmTypeEffects (typeID)",
		"CREATE INDEX IF NOT EXISTS idx_dte_effect ON dgmTypeEffects (effectID)",
		"CREATE INDEX IF NOT EXISTS idx_dat_name ON dgmAttributeTypes (attributeName)",
		"CREATE INDEX IF NOT EXISTS idx_de_name ON dgmEffects (effectName)",
		"CREATE INDEX IF NOT EXISTS idx_ig_cat ON invGroups (categoryID)",
		"CREATE INDEX IF NOT EXISTS idx_ig_name ON invGroups (groupName)",
		"CREATE INDEX IF NOT EXISTS idx_ic_name ON invCategories (categoryName)",
		"CREATE INDEX IF NOT EXISTS idx_chrf_name ON chrFactions (factionName)",
		"CREATE INDEX IF NOT EXISTS idx_chrr_name ON chrRaces (raceName)",
		"CREATE INDEX IF NOT EXISTS idx_iapb_bp ON industryActivityProbabilities (blueprint_typeID, activityID)",
		"CREATE INDEX IF NOT EXISTS idx_img_parent ON invMarketGroups (parentGroupID)",
		"CREATE INDEX IF NOT EXISTS idx_img_name ON invMarketGroups (marketGroupName)",
		"CREATE INDEX IF NOT EXISTS idx_crp_faction ON crpNPCCorporations (factionID)",
	}
	for _, idx := range curatedIndexes {
		if _, err := db.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("build sde sqlite: index: %w", err)
		}
	}

	// Generic-table indexes.
	// These are best-effort; the tables may not exist in a curated-only run.
	genericIndexes := [][3]string{
		{"idx_invtraits_type", "invTraits", "typeID"},
		{"idx_invmetatypes_type", "invMetaTypes", "typeID"},
		{"idx_invnames_item", "invNames", "itemID"},
		{"idx_agt_corp", "agtAgents", "corporationID"},
		{"idx_agt_level", "agtAgents", "level"},
		{"idx_agt_loc", "agtAgents", "locationID"},
		{"idx_agt_division", "agtAgents", "divisionID"},
		{"idx_agt_type", "agtAgents", "agentTypeID"},
		{"idx_agtypes_id", "agtAgentTypes", "agentTypeID"},
	}
	for _, g := range genericIndexes {
		q := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON "%s" ("%s")`, g[0], g[1], g[2])
		if _, err := db.ExecContext(ctx, q); err != nil {
			// Best-effort; table may not exist in curated-only mode.
			fmt.Printf("sde build: generic index %q skipped: %v\n", g[0], err)
		}
	}

	// Publish gate: a build that cannot serve the readers never reaches the live
	// path. Returning here leaves dstPath untouched; the deferred cleanup removes
	// the temp file.
	if err := gate.check(ctx, db); err != nil {
		slog.Error("sde build: publish gate rejected the new build, keeping the live file", "path", dstPath, "error", err)
		return fmt.Errorf("build sde sqlite: publish gate: %w", err)
	}

	// Close before rename so the file is fully flushed.
	if err := db.Close(); err != nil {
		return fmt.Errorf("build sde sqlite: close temp db: %w", err)
	}

	// Atomic rename: never exposes a partial file to readers.
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return fmt.Errorf("build sde sqlite: rename to %q: %w", dstPath, err)
	}
	return nil
}

// loadCurated streams one curated CSV into the database.
func loadCurated(ctx context.Context, db *sql.DB, tbl curatedTable, fw Client) error {
	if _, err := db.ExecContext(ctx, tbl.create); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	rc, err := fw.DownloadTable(ctx, tbl.name)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer rc.Close()

	r := csv.NewReader(rc)
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	// Read header row.
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	// Build column-name → CSV-index map.
	colIdx := make(map[string]int, len(header))
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	stmt, err := db.PrepareContext(ctx, tbl.insert)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txStmt := tx.Stmt(stmt)
	n := 0

	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Skip malformed rows (lenient by design).
			continue
		}

		// Build a map[colName]value for the extract function.
		rowMap := make(map[string]string, len(tbl.columns))
		for _, col := range tbl.columns {
			if idx, ok := colIdx[col]; ok && idx < len(row) {
				rowMap[col] = row[idx]
			}
		}

		vals, ok := tbl.extract(rowMap)
		if !ok {
			continue
		}

		if _, err := txStmt.ExecContext(ctx, vals...); err != nil {
			// Ignore duplicate-key violations (INSERT OR REPLACE semantics).
			continue
		}
		n++
		if n%batchSize == 0 {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit batch: %w", err)
			}
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin next tx: %w", err)
			}
			txStmt = tx.Stmt(stmt)
		}
	}

	return tx.Commit()
}

// identRE matches valid SQLite identifiers so we can skip re-sanitising them.
var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// safeCol sanitises a CSV header cell into a SQLite-safe identifier.
func safeCol(name string) string {
	cleaned := strings.TrimSpace(name)
	if identRE.MatchString(cleaned) {
		return cleaned
	}
	safe := regexp.MustCompile(`[^A-Za-z0-9_]`).ReplaceAllString(cleaned, "_")
	if safe == "" {
		safe = "col"
	}
	if safe[0] >= '0' && safe[0] <= '9' {
		safe = "_" + safe
	}
	return safe
}

// loadGeneric streams one generic CSV into the database with all-TEXT columns.
// Empty cell values become NULL.
func loadGeneric(ctx context.Context, db *sql.DB, name string, fw Client) error {
	rc, err := fw.DownloadTable(ctx, name)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer rc.Close()

	r := csv.NewReader(rc)
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	// Read header.
	rawHeader, err := r.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	if len(rawHeader) == 0 {
		return nil // empty CSV
	}

	// Sanitise and deduplicate column names.
	seenCols := make(map[string]int, len(rawHeader))
	cols := make([]string, 0, len(rawHeader))
	for _, h := range rawHeader {
		c := safeCol(h)
		if cnt, exists := seenCols[c]; exists {
			seenCols[c]++
			cols = append(cols, fmt.Sprintf("%s_%d", c, cnt+1))
		} else {
			seenCols[c] = 1
			cols = append(cols, c)
		}
	}

	// DROP + CREATE TABLE with TEXT columns.
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = `"` + c + `" TEXT`
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, name)); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE "%s" (%s)`, name, strings.Join(quotedCols, ", "))); err != nil {
		return err
	}

	placeholders := strings.Repeat("?,", len(cols))
	placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
	insertSQL := fmt.Sprintf(`INSERT INTO "%s" VALUES (%s)`, name, placeholders)

	stmt, err := db.PrepareContext(ctx, insertSQL)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txStmt := tx.Stmt(stmt)
	n := 0
	width := len(cols)

	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		// Pad / truncate to match header width.
		for len(row) < width {
			row = append(row, "")
		}
		if len(row) > width {
			row = row[:width]
		}

		// Convert empty strings to nil (NULL).
		vals := make([]any, width)
		for i, v := range row {
			if v == "" {
				vals[i] = nil
			} else {
				vals[i] = v
			}
		}

		if _, err := txStmt.ExecContext(ctx, vals...); err != nil {
			continue // skip bad rows
		}
		n++
		if n%batchSize == 0 {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit batch: %w", err)
			}
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin next tx: %w", err)
			}
			txStmt = tx.Stmt(stmt)
		}
	}

	return tx.Commit()
}
