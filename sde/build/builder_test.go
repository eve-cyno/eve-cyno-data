// core/sde/build/builder_test.go
//
// Unit tests for BuildSQLite with tiny inline CSV fixtures. Does NOT hit the
// network or use the real 506 MB SDE dump.
package build

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	coresde "eve-cyno.dev/go/data/sde"
)

// fakeFuzzwork is an in-memory Client for tests.
type fakeFuzzwork struct {
	tables map[string]string // table name → CSV text (uncompressed)
}

func (f *fakeFuzzwork) LatestBuild(_ context.Context) (string, error) {
	return "sde-20250101-TRANQUILITY", nil
}

func (f *fakeFuzzwork) ListTables(_ context.Context) ([]string, error) {
	names := make([]string, 0, len(f.tables))
	for k := range f.tables {
		names = append(names, k)
	}
	return names, nil
}

func (f *fakeFuzzwork) DownloadTable(_ context.Context, name string) (io.ReadCloser, error) {
	data, ok := f.tables[name]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	return io.NopCloser(strings.NewReader(data)), nil
}

// Real Fuzzwork CSV headers (2026-10 dump) for the tables that carry the columns
// the readers need. A row whose field count differs from the header is silently
// skipped by the builder, so fixtures are written through csvTable.
var (
	invTypesHeader = []string{"typeID", "groupID", "typeName", "description", "mass", "volume", "capacity",
		"portionSize", "raceID", "basePrice", "published", "marketGroupID", "iconID", "soundID", "graphicID",
		"factionID", "metaLevel", "techLevel", "shipTreeGroupID", "packagedVolume", "isDynamicType", "isRepackable"}

	dgmEffectsHeader = []string{"effectID", "effectName", "effectCategory", "preExpression", "postExpression",
		"description", "guid", "iconID", "isOffensive", "isAssistance", "durationAttributeID",
		"trackingSpeedAttributeID", "dischargeAttributeID", "rangeAttributeID", "falloffAttributeID",
		"disallowAutoRepeat", "published", "displayName", "isWarpSafe", "rangeChance", "electronicChance",
		"propulsionChance", "distribution", "sfxName", "npcUsageChanceAttributeID",
		"npcActivationChanceAttributeID", "fittingUsageChanceAttributeID", "modifierInfo"}

	dgmAttributeTypesHeader = []string{"attributeID", "attributeName", "description", "iconID", "defaultValue",
		"published", "displayName", "unitID", "stackable", "highIsGood", "categoryID"}
)

// rifterBonusModifierInfo is effect 414's modifierInfo exactly as Fuzzwork serves it.
const rifterBonusModifierInfo = `[{"domain": "shipID", "func": "LocationRequiredSkillModifier", "modifiedAttributeID": 51, "modifyingAttributeID": 441, "operation": 6, "skillTypeID": 3300}]`

// csvTable renders header + rows (keyed by column name, missing keys become empty
// cells) as CSV text with proper quoting.
func csvTable(t *testing.T, header []string, rows ...map[string]string) string {
	t.Helper()
	var b strings.Builder
	w := csv.NewWriter(&b)
	require.NoError(t, w.Write(header))
	for _, r := range rows {
		rec := make([]string, len(header))
		for i, h := range header {
			rec[i] = r[h]
		}
		require.NoError(t, w.Write(rec))
	}
	w.Flush()
	require.NoError(t, w.Error())
	return b.String()
}

// minimalFuzzwork returns a fake that provides tiny CSVs for every curated table
// plus one extra table for the generic phase.
func minimalFuzzwork(t *testing.T) *fakeFuzzwork {
	t.Helper()
	tables := map[string]string{
		// Curated tables — one row each (headers must match the columns[] we use).
		"invTypes": csvTable(t, invTypesHeader,
			map[string]string{"typeID": "587", "groupID": "25", "typeName": "Rifter", "description": "A fast frigate",
				"mass": "1067000.0", "volume": "27289.0", "capacity": "140.0", "published": "1"},
			// A type without mass/volume/capacity: the three columns must be NULL, not 0.
			map[string]string{"typeID": "9999", "groupID": "25", "typeName": "Massless", "published": "0"}),
		"dgmTypeAttributes": "typeID,attributeID,valueInt,valueFloat\n587,11,50.0,\n",
		"dgmTypeEffects":    "typeID,effectID,isDefault\n587,12,1\n",
		"dgmEffects": csvTable(t, dgmEffectsHeader,
			map[string]string{"effectID": "12", "effectName": "hiPower", "effectCategory": "0", "published": "1", "displayName": "High power slot"},
			map[string]string{"effectID": "414", "effectName": "skillBonusFrigate", "effectCategory": "0", "published": "0",
				"modifierInfo": rifterBonusModifierInfo}),
		"dgmAttributeTypes": csvTable(t, dgmAttributeTypesHeader,
			map[string]string{"attributeID": "11", "attributeName": "powerOutput", "iconID": "0", "defaultValue": "0", "published": "1",
				"displayName": "Power Output", "unitID": "107", "stackable": "0", "highIsGood": "1", "categoryID": "1"},
			map[string]string{"attributeID": "51", "attributeName": "speed", "defaultValue": "0.0", "published": "1",
				"stackable": "0", "highIsGood": "0"},
			map[string]string{"attributeID": "70", "attributeName": "agility", "defaultValue": "3.5", "published": "1",
				"stackable": "1", "highIsGood": "0"},
			// stackable / highIsGood / defaultValue empty → NULL.
			map[string]string{"attributeID": "71", "attributeName": "noMeta", "published": "0"}),
		"invGroups":                     "groupID,categoryID,groupName,iconID,useBasePrice,anchored,anchorable,fittableNonSingleton,published\n25,6,Frigate,0,0,0,0,0,1\n",
		"invCategories":                 "categoryID,categoryName,iconID,published\n6,Ship,0,1\n",
		"eveUnits":                      "unitID,unitName,displayName,description\n107,MW,MW,Megawatts\n",
		"chrFactions":                   "factionID,factionName,description,raceIDs,solarSystemID,corporationID,sizeFactor,stationCount,stationSystemCount,militiaCorporationID,iconID\n500002,Minmatar Republic,,7,30002544,1000051,5,1000,400,1000182,0\n",
		"chrRaces":                      "raceID,raceName,description,iconID,shortDescription\n2,Minmatar,Warriors,0,Speed and agility\n",
		"chrCloneGrades":                "cloneGradeID,name\n1,Alpha Caldari\n",
		"chrCloneGradeSkills":           "cloneGradeID,typeID,level\n1,3300,5\n",
		"industryActivity":              "typeID,activityID,time\n587,1,3600\n",
		"industryActivityMaterials":     "typeID,activityID,materialTypeID,quantity\n587,1,34,100\n",
		"industryActivityProducts":      "typeID,activityID,productTypeID,quantity,probability\n587,1,588,1,\n",
		"industryActivitySkills":        "typeID,activityID,skillID,level\n587,1,3380,1\n",
		"invTypeMaterials":              "typeID,materialTypeID,quantity\n587,34,50\n",
		"mapSolarSystems":               "regionID,constellationID,solarSystemID,solarSystemName,x,y,z,xMin,xMax,yMin,yMax,zMin,zMax,luminosity,border,fringe,corridor,hub,international,regional,constellation,security,factionID,radius,sunTypeID,securityClass\n10000042,20000607,30004759,Rens,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0.9,500002,0,0,B\n",
		"mapSolarSystemJumps":           "fromRegionID,fromConstellationID,fromSolarSystemID,toSolarSystemID,toConstellationID,toRegionID\n10000042,20000607,30004759,30004760,20000607,10000042\n",
		"mapRegions":                    "regionID,regionName,x,y,z,xMin,xMax,yMin,yMax,zMin,zMax,factionID,nebula,radius\n10000042,Heimatar,0,0,0,0,0,0,0,0,0,500002,0,0\n",
		"mapConstellations":             "regionID,constellationID,constellationName,x,y,z,xMin,xMax,yMin,yMax,zMin,zMax,factionID,radius\n10000042,20000607,Ani,0,0,0,0,0,0,0,0,0,500002,0\n",
		"staStations":                   "stationID,security,dockingCostPerVolume,maxShipVolumeDockable,officeRentalCost,operationID,stationTypeID,corporationID,solarSystemID,constellationID,regionID,stationName,x,y,z,reprocessingEfficiency,reprocessingStationsTake,reprocessingHangarFlag\n60004579,0.9,0,50000000,10000,7,52,1000051,30004759,20000607,10000042,Rens - Business Center,0,0,0,0.5,0.075,4\n",
		"staOperationServices":          "operationID,serviceID\n7,1\n",
		"staServices":                   "serviceID,serviceName,description\n1,Market,Trade your goods here\n",
		"industryActivityProbabilities": "typeID,activityID,productTypeID,probability\n587,8,589,0.3\n",
		"invMarketGroups":               "marketGroupID,parentGroupID,marketGroupName,description,iconID,hasTypes\n4,2,Ships,,0,0\n",
		"crpNPCCorporations":            "corporationID,size,extent,solarSystemID,investorID1,investorShares1,investorID2,investorShares2,investorID3,investorShares3,investorID4,investorShares4,friendID,enemyID,publicShares,initialPrice,minSecurity,scattered,fringe,corridor,hub,border,factionID,sizeFactor,stationCount,stationSystemCount,description,iconID\n1000051,L,R,30004759,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,500002,5,10,5,Brutor Tribe,0\n",
		// Extra generic table (not in curatedSet).
		"extraTable": "colA,colB,colC\nalpha,beta,gamma\ndelta,,zeta\n",
	}
	return &fakeFuzzwork{tables: tables}
}

// fixtureGate is satisfied by minimalFuzzwork (one modifierInfo row, one type with mass).
var fixtureGate = Gate{minModifierInfoRows: 1, minMassRows: 1}

// syncBuffer is a bytes.Buffer safe for concurrent slog writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog routes the default slog logger into a buffer for the test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func openRO(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestBuildSQLite_CuratedRows(t *testing.T) {
	tmp := t.TempDir()
	dst := tmp + "/sde.sqlite"
	fw := minimalFuzzwork(t)

	require.NoError(t, buildSQLite(context.Background(), dst, fw, fixtureGate))

	db, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()

	// invTypes: row count and values.
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM invTypes").Scan(&n))
	require.Equal(t, 2, n, "invTypes should have 2 rows")

	var typeID, groupID int
	var typeName string
	var published int
	require.NoError(t, db.QueryRow("SELECT typeID, groupID, typeName, published FROM invTypes WHERE typeID=587").
		Scan(&typeID, &groupID, &typeName, &published))
	require.Equal(t, 587, typeID)
	require.Equal(t, 25, groupID)
	require.Equal(t, "Rifter", typeName)
	require.Equal(t, 1, published)

	// dgmTypeAttributes: NULL handling (valueFloat is empty → NULL).
	var vi float64
	var vf *float64
	require.NoError(t, db.QueryRow("SELECT valueInt, valueFloat FROM dgmTypeAttributes WHERE typeID=587 AND attributeID=11").
		Scan(&vi, &vf))
	require.InDelta(t, 50.0, vi, 0.001)
	require.Nil(t, vf, "empty valueFloat should be NULL")

	// mapSolarSystems: security (REAL) and factionID (INTEGER).
	var sec float64
	var faction int
	require.NoError(t, db.QueryRow("SELECT security, factionID FROM mapSolarSystems WHERE solarSystemID=30004759").
		Scan(&sec, &faction))
	require.InDelta(t, 0.9, sec, 0.001)
	require.Equal(t, 500002, faction)

	// industryActivity: blueprint_typeID column (renamed from typeID in CSV).
	var bpType, actID int
	require.NoError(t, db.QueryRow("SELECT blueprint_typeID, activityID FROM industryActivity WHERE blueprint_typeID=587").
		Scan(&bpType, &actID))
	require.Equal(t, 587, bpType)
	require.Equal(t, 1, actID)
}

// The dogma readers need invTypes.mass/volume/capacity, dgmEffects.modifierInfo and
// dgmAttributeTypes.defaultValue/stackable/highIsGood. The Python->Go port dropped
// them and every reader failed silently (no skill/ship modifiers reached the dogma
// engine). This pins the values and reads them back through the real readers.
func TestBuildSQLite_ReaderColumns(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "sde.sqlite")
	require.NoError(t, buildSQLite(context.Background(), dst, minimalFuzzwork(t), fixtureGate))

	db := openRO(t, dst)

	// invTypes.mass/volume/capacity: REAL values; empty cells are NULL, not 0.
	var mass, volume, capacity sql.NullFloat64
	require.NoError(t, db.QueryRow("SELECT mass, volume, capacity FROM invTypes WHERE typeID=587").Scan(&mass, &volume, &capacity))
	require.Equal(t, sql.NullFloat64{Float64: 1067000, Valid: true}, mass)
	require.Equal(t, sql.NullFloat64{Float64: 27289, Valid: true}, volume)
	require.Equal(t, sql.NullFloat64{Float64: 140, Valid: true}, capacity)
	require.NoError(t, db.QueryRow("SELECT mass, volume, capacity FROM invTypes WHERE typeID=9999").Scan(&mass, &volume, &capacity))
	require.False(t, mass.Valid, "empty mass must be NULL")
	require.False(t, volume.Valid, "empty volume must be NULL")
	require.False(t, capacity.Valid, "empty capacity must be NULL")

	// dgmEffects.modifierInfo is stored verbatim; empty is NULL.
	var mi sql.NullString
	require.NoError(t, db.QueryRow("SELECT modifierInfo FROM dgmEffects WHERE effectID=414").Scan(&mi))
	require.Equal(t, rifterBonusModifierInfo, mi.String)
	require.NoError(t, db.QueryRow("SELECT modifierInfo FROM dgmEffects WHERE effectID=12").Scan(&mi))
	require.False(t, mi.Valid, "empty modifierInfo must be NULL")

	// dgmAttributeTypes.defaultValue/stackable/highIsGood.
	var def sql.NullFloat64
	var stackable, highIsGood sql.NullInt64
	q := "SELECT defaultValue, stackable, highIsGood FROM dgmAttributeTypes WHERE attributeID=?"
	require.NoError(t, db.QueryRow(q, 70).Scan(&def, &stackable, &highIsGood))
	require.Equal(t, sql.NullFloat64{Float64: 3.5, Valid: true}, def)
	require.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, stackable)
	require.Equal(t, sql.NullInt64{Int64: 0, Valid: true}, highIsGood)
	require.NoError(t, db.QueryRow(q, 71).Scan(&def, &stackable, &highIsGood))
	require.False(t, def.Valid)
	require.False(t, stackable.Valid)
	require.False(t, highIsGood.Valid)

	// The built file satisfies the shared required-column list.
	missing, err := coresde.MissingRequiredColumns(db)
	require.NoError(t, err)
	require.Empty(t, missing)

	// And the production readers see the data (the user-visible symptom).
	s, err := coresde.OpenWithReload(dst, 0)
	require.NoError(t, err)
	defer s.Close()

	mods := s.GetEffectModifiers(414)
	require.Len(t, mods, 1, "modifierInfo must parse with the core/sde JSON reader")
	require.Equal(t, "shipID", mods[0].Domain)
	require.Equal(t, "LocationRequiredSkillModifier", mods[0].Func)
	require.Equal(t, 51, mods[0].ModifiedAttr)
	require.Equal(t, 441, mods[0].ModifyingAttr)
	require.Equal(t, 6, mods[0].Operation)
	require.NotNil(t, mods[0].SkillTypeID)
	require.Equal(t, 3300, *mods[0].SkillTypeID)

	require.Equal(t, coresde.AttrMeta{Stackable: true, HighIsGood: false, Name: "agility"}, s.GetAttributeMeta(70))
	require.Equal(t, coresde.AttrMeta{Stackable: false, HighIsGood: false, Name: "speed"}, s.GetAttributeMeta(51))

	dogma := s.GetDogma(587)
	require.InDelta(t, 1067000, dogma[4], 0.001, "mass merged as attr 4")
	require.InDelta(t, 140, dogma[38], 0.001, "capacity merged as attr 38")
	require.InDelta(t, 27289, dogma[161], 0.001, "volume merged as attr 161")
}

func TestBuildSQLite_GenericTable(t *testing.T) {
	tmp := t.TempDir()
	dst := tmp + "/sde.sqlite"
	fw := minimalFuzzwork(t)

	require.NoError(t, buildSQLite(context.Background(), dst, fw, fixtureGate))

	db, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()

	// extraTable should exist with TEXT columns.
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM "extraTable"`).Scan(&n))
	require.Equal(t, 2, n, "extraTable should have 2 data rows")

	var a, b, c any
	require.NoError(t, db.QueryRow(`SELECT colA, colB, colC FROM "extraTable" LIMIT 1`).Scan(&a, &b, &c))
	require.Equal(t, "alpha", a)

	// Second row: colB was empty → NULL.
	require.NoError(t, db.QueryRow(`SELECT colB FROM "extraTable" WHERE colA='delta'`).Scan(&b))
	require.Nil(t, b, "empty cell should be NULL in generic table")
}

func TestBuildSQLite_AtomicRename(t *testing.T) {
	tmp := t.TempDir()
	dst := tmp + "/sde.sqlite"
	fw := minimalFuzzwork(t)

	require.NoError(t, buildSQLite(context.Background(), dst, fw, fixtureGate))

	// The destination file must exist; the .tmp file must be gone.
	_, err := os.Stat(dst)
	require.NoError(t, err, "destination file should exist")
	_, err = os.Stat(dst + ".tmp")
	require.True(t, os.IsNotExist(err), ".tmp file should be cleaned up after rename")
}

// A bare file name (EVE_CORE_SDE_PATH=sde.sqlite, relative to the working directory)
// used to panic slicing the directory out of the path; it builds in the CWD now.
func TestBuildSQLite_BareFileName(t *testing.T) {
	t.Chdir(t.TempDir())

	require.NoError(t, buildSQLite(context.Background(), "sde.sqlite", minimalFuzzwork(t), fixtureGate))

	_, err := os.Stat("sde.sqlite")
	require.NoError(t, err)
}

func TestBuildSQLite_NullHandling(t *testing.T) {
	tmp := t.TempDir()
	dst := tmp + "/sde.sqlite"

	// Minimal fake: only the tables we need to check NULL handling.
	fw := &fakeFuzzwork{
		tables: map[string]string{
			"invTypes": "typeID,groupID,typeName,published\n100,,MyType,0\n",
			// All other curated tables are empty (header only).
			"dgmTypeAttributes":             "typeID,attributeID,valueInt,valueFloat\n",
			"dgmTypeEffects":                "typeID,effectID,isDefault\n",
			"dgmEffects":                    "effectID,effectName,effectCategory,displayName,published\n",
			"dgmAttributeTypes":             "attributeID,attributeName,displayName,unitID,published\n",
			"invGroups":                     "groupID,categoryID,groupName,published\n",
			"invCategories":                 "categoryID,categoryName,published\n",
			"eveUnits":                      "unitID,unitName,displayName,description\n",
			"chrFactions":                   "factionID,factionName,solarSystemID,corporationID,militiaCorporationID\n",
			"chrRaces":                      "raceID,raceName,shortDescription\n",
			"chrCloneGrades":                "cloneGradeID,name\n1,Alpha Caldari\n",
			"chrCloneGradeSkills":           "cloneGradeID,typeID,level\n1,3300,5\n",
			"industryActivity":              "typeID,activityID,time\n",
			"industryActivityMaterials":     "typeID,activityID,materialTypeID,quantity\n",
			"industryActivityProducts":      "typeID,activityID,productTypeID,quantity,probability\n",
			"industryActivitySkills":        "typeID,activityID,skillID,level\n",
			"invTypeMaterials":              "typeID,materialTypeID,quantity\n",
			"mapSolarSystems":               "solarSystemID,regionID,constellationID,solarSystemName,security,securityClass,factionID\n",
			"mapSolarSystemJumps":           "fromSolarSystemID,toSolarSystemID\n",
			"mapRegions":                    "regionID,regionName,factionID\n",
			"mapConstellations":             "constellationID,regionID,constellationName,factionID\n",
			"staStations":                   "stationID,solarSystemID,regionID,corporationID,stationTypeID,stationName,operationID\n",
			"staOperationServices":          "operationID,serviceID\n",
			"staServices":                   "serviceID,serviceName,description\n",
			"industryActivityProbabilities": "typeID,activityID,productTypeID,probability\n",
			"invMarketGroups":               "marketGroupID,parentGroupID,marketGroupName,hasTypes\n",
			"crpNPCCorporations":            "corporationID,factionID,solarSystemID,description\n",
		},
	}

	// Deliberately degenerate fixture: the gate is disabled here (zero thresholds).
	require.NoError(t, buildSQLite(context.Background(), dst, fw, Gate{}))

	db, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()

	// groupID was empty → NULL.
	var typeID int
	var groupID *int64
	require.NoError(t, db.QueryRow("SELECT typeID, groupID FROM invTypes WHERE typeID=100").Scan(&typeID, &groupID))
	require.Equal(t, 100, typeID)
	require.Nil(t, groupID, "empty groupID should be NULL")
}

// ── publish gate ────────────────────────────────────────────────────────────

// writeLiveSDE puts a sentinel "live" SDE file at path so tests can prove a
// rejected build leaves it untouched.
func writeLiveSDE(t *testing.T, path string) []byte {
	t.Helper()
	content := []byte("live sde - must survive a rejected build")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return content
}

func requireLiveSDEUntouched(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, got, "a rejected build must not replace the live file")
	_, err = os.Stat(path + ".tmp")
	require.True(t, os.IsNotExist(err), "the rejected temp build must be removed")
}

func TestBuildSQLite_PublishGate_RejectsBuildWithoutModifierInfo(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "sde.sqlite")
	live := writeLiveSDE(t, dst)
	buf := captureLog(t)

	// The dgmEffects CSV carries no modifierInfo values (the regression: the data
	// never reached the table).
	fw := minimalFuzzwork(t)
	fw.tables["dgmEffects"] = csvTable(t, dgmEffectsHeader,
		map[string]string{"effectID": "12", "effectName": "hiPower", "effectCategory": "0", "published": "1"})

	err := buildSQLite(context.Background(), dst, fw, fixtureGate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "modifierInfo")

	requireLiveSDEUntouched(t, dst, live)

	var errLines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=ERROR") {
			errLines = append(errLines, line)
		}
	}
	require.Len(t, errLines, 1, "the rejection is logged once at ERROR: %v", errLines)
	require.Contains(t, errLines[0], "modifierInfo")
}

func TestBuildSQLite_PublishGate_RejectsBuildWithoutMass(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "sde.sqlite")
	live := writeLiveSDE(t, dst)

	fw := minimalFuzzwork(t)
	fw.tables["invTypes"] = csvTable(t, invTypesHeader,
		map[string]string{"typeID": "587", "groupID": "25", "typeName": "Rifter", "mass": "0", "published": "1"})

	err := buildSQLite(context.Background(), dst, fw, fixtureGate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mass")
	requireLiveSDEUntouched(t, dst, live)
}

// The exported entry point is gated with the production thresholds: a tiny fixture must be rejected.
func TestBuildSQLite_DefaultGate_RejectsDegenerateBuild(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "sde.sqlite")
	live := writeLiveSDE(t, dst)

	err := BuildSQLite(context.Background(), dst, minimalFuzzwork(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "publish gate")
	requireLiveSDEUntouched(t, dst, live)
}

func TestDefaultGate_Thresholds(t *testing.T) {
	g := DefaultGate()
	require.GreaterOrEqual(t, g.minModifierInfoRows, 1000)
	require.GreaterOrEqual(t, g.minMassRows, 1000)
}

func TestPublishGate_RejectsMissingRequiredColumn(t *testing.T) {
	// A schema as the Python->Go port left it: no modifierInfo on dgmEffects.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE invTypes (typeID INTEGER PRIMARY KEY, groupID INTEGER, typeName TEXT, published INTEGER, mass REAL, volume REAL, capacity REAL)`,
		`CREATE TABLE dgmEffects (effectID INTEGER PRIMARY KEY, effectName TEXT, effectCategory INTEGER, displayName TEXT, published INTEGER)`,
		`CREATE TABLE dgmAttributeTypes (attributeID INTEGER PRIMARY KEY, attributeName TEXT, displayName TEXT, unitID INTEGER, published INTEGER)`,
	} {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}

	err = Gate{}.check(context.Background(), db)
	require.Error(t, err)
	require.Contains(t, err.Error(), "dgmEffects.modifierInfo")
	require.Contains(t, err.Error(), "dgmAttributeTypes.stackable")
	require.Contains(t, err.Error(), "chrCloneGradeSkills.level", "the Alpha clone skill tables are required too")
}

func TestPublishGate_PassesCompleteBuild(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "sde.sqlite")
	require.NoError(t, buildSQLite(context.Background(), dst, minimalFuzzwork(t), fixtureGate))

	require.NoError(t, fixtureGate.check(context.Background(), openRO(t, dst)))
}
