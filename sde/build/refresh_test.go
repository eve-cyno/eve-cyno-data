// core/sde/build/refresh_test.go
//
// Unit tests for Builder.Refresh with injected fake Fuzzwork remotes.
// Does NOT hit the network or perform real downloads.
package build

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeRemote is a Remote with a configurable latest build that records whether a
// build ran (ListTables is the first thing buildSQLite asks the remote after the
// curated tables, so a call means "a rebuild was triggered").
type fakeRemote struct {
	fakeFuzzwork
	latest    string
	latestErr error
	built     bool
}

func (f *fakeRemote) LatestBuild(_ context.Context) (string, error) {
	return f.latest, f.latestErr
}

func (f *fakeRemote) ListTables(ctx context.Context) ([]string, error) {
	f.built = true
	return f.fakeFuzzwork.ListTables(ctx)
}

// allCuratedEmpty returns a table map where every curated table exists but is
// header-only (no data rows), and a single generic table for phase 2.
func allCuratedEmpty() map[string]string {
	return map[string]string{
		"invTypes":                      "typeID,groupID,typeName,published\n",
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
	}
}

func TestRefresh_UnchangedBuild_NoOp(t *testing.T) {
	const build = "sde-20250101-TRANQUILITY"

	fr := &fakeRemote{fakeFuzzwork: fakeFuzzwork{tables: allCuratedEmpty()}, latest: build}
	b := NewWithRemote(fr, filepath.Join(t.TempDir(), "sde.sqlite"), Gate{})

	latest, rebuilt, err := b.Refresh(context.Background(), build)
	require.NoError(t, err)

	require.Equal(t, build, latest)
	require.False(t, rebuilt, "nothing to do when the build is unchanged")
	require.False(t, fr.built, "BuildSQLite should not be called when build is unchanged")
}

func TestRefresh_ChangedBuild_Rebuilds(t *testing.T) {
	const oldBuild = "sde-20250101-TRANQUILITY"
	const newBuild = "sde-20250707-TRANQUILITY"

	fr := &fakeRemote{fakeFuzzwork: fakeFuzzwork{tables: allCuratedEmpty()}, latest: newBuild}
	sdePath := filepath.Join(t.TempDir(), "sde.sqlite")
	b := NewWithRemote(fr, sdePath, Gate{})

	latest, rebuilt, err := b.Refresh(context.Background(), oldBuild)
	require.NoError(t, err)

	require.Equal(t, newBuild, latest)
	require.True(t, rebuilt)
	require.True(t, fr.built, "BuildSQLite should be called when build changes")
	_, statErr := os.Stat(sdePath)
	require.NoError(t, statErr, "the rebuilt file is published at the SDE path")
}

// A fresh start has no recorded build: "" differs from any real build.
func TestRefresh_NoRecordedBuild_Rebuilds(t *testing.T) {
	const newBuild = "sde-20250707-TRANQUILITY"

	fr := &fakeRemote{fakeFuzzwork: fakeFuzzwork{tables: allCuratedEmpty()}, latest: newBuild}
	b := NewWithRemote(fr, filepath.Join(t.TempDir(), "sde.sqlite"), Gate{})

	latest, rebuilt, err := b.Refresh(context.Background(), "")
	require.NoError(t, err)

	require.Equal(t, newBuild, latest)
	require.True(t, rebuilt)
	require.True(t, fr.built)
}

func TestRefresh_LatestBuildError(t *testing.T) {
	boom := errors.New("fuzzwork down")
	fr := &fakeRemote{fakeFuzzwork: fakeFuzzwork{tables: allCuratedEmpty()}, latestErr: boom}
	b := NewWithRemote(fr, filepath.Join(t.TempDir(), "sde.sqlite"), Gate{})

	_, rebuilt, err := b.Refresh(context.Background(), "")
	require.ErrorIs(t, err, boom)
	require.Contains(t, err.Error(), "fetch latest build")
	require.False(t, rebuilt)
	require.False(t, fr.built)
}

// The production gate refuses a degenerate rebuild: the live file survives and no
// build is reported, so the caller's recorded build does not advance and the next
// run retries.
func TestRefresh_PublishGateRejection_KeepsLiveFile(t *testing.T) {
	sdePath := filepath.Join(t.TempDir(), "sde.sqlite")
	live := []byte("live sde - must survive a rejected build")
	require.NoError(t, os.WriteFile(sdePath, live, 0o644))

	fr := &fakeRemote{fakeFuzzwork: fakeFuzzwork{tables: allCuratedEmpty()}, latest: "sde-20250707-TRANQUILITY"}
	b := NewWithRemote(fr, sdePath, DefaultGate())

	_, rebuilt, err := b.Refresh(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "publish gate")
	require.False(t, rebuilt, "the recorded build must not advance on a rejected build")

	got, readErr := os.ReadFile(sdePath)
	require.NoError(t, readErr)
	require.Equal(t, live, got)
}

func TestNew_UsesProductionPublishGate(t *testing.T) {
	require.Equal(t, DefaultGate(), New("x").gate)
}

// writeOldSchemaSDE writes an SDE file as the Python->Go port left it: row counts
// are fine but dgmEffects.modifierInfo (and friends) are missing.
func writeOldSchemaSDE(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE invTypes (typeID INTEGER PRIMARY KEY, groupID INTEGER, typeName TEXT, published INTEGER)`,
		`CREATE TABLE dgmEffects (effectID INTEGER PRIMARY KEY, effectName TEXT, effectCategory INTEGER, displayName TEXT, published INTEGER)`,
		`CREATE TABLE dgmAttributeTypes (attributeID INTEGER PRIMARY KEY, attributeName TEXT, displayName TEXT, unitID INTEGER, published INTEGER)`,
	} {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
}

// A deployed SDE built before the fix keeps its (unchanged) Fuzzwork build id, so
// without this the broken file would live until the next monthly dump. Refresh must
// notice the missing columns and rebuild at the same build.
func TestRefresh_UnchangedBuild_RebuildsWhenLiveFileLacksRequiredColumns(t *testing.T) {
	const build = "sde-20250101-TRANQUILITY"

	sdePath := filepath.Join(t.TempDir(), "sde.sqlite")
	writeOldSchemaSDE(t, sdePath)

	fr := &fakeRemote{fakeFuzzwork: fakeFuzzwork{tables: minimalFuzzwork(t).tables}, latest: build}
	b := NewWithRemote(fr, sdePath, fixtureGate)

	latest, rebuilt, err := b.Refresh(context.Background(), build)
	require.NoError(t, err)
	require.True(t, fr.built, "a live file lacking required columns must be rebuilt even when the build is unchanged")
	require.True(t, rebuilt)
	require.Equal(t, build, latest)

	// The repaired file now has the columns.
	db := openRO(t, sdePath)
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM dgmEffects WHERE modifierInfo IS NOT NULL`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestRefresh_UnchangedBuild_HealthyLiveFileIsNotRebuilt(t *testing.T) {
	const build = "sde-20250101-TRANQUILITY"

	sdePath := filepath.Join(t.TempDir(), "sde.sqlite")
	require.NoError(t, buildSQLite(context.Background(), sdePath, minimalFuzzwork(t), fixtureGate))

	fr := &fakeRemote{fakeFuzzwork: fakeFuzzwork{tables: minimalFuzzwork(t).tables}, latest: build}
	b := NewWithRemote(fr, sdePath, fixtureGate)

	_, rebuilt, err := b.Refresh(context.Background(), build)
	require.NoError(t, err)
	require.False(t, fr.built)
	require.False(t, rebuilt)
}
