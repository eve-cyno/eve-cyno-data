package tools

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde"
)

// tinyTypesSDE is an SDE with three types (Tritanium 34, Rifter 587, PLEX 44992): enough for
// the type_id preflight, no real SDE needed.
func tinyTypesSDE(t *testing.T) *sde.SDE {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sde.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE invTypes (typeID INTEGER, groupID INTEGER, typeName TEXT, published INTEGER, mass REAL, volume REAL, capacity REAL)`,
		`CREATE TABLE dgmEffects (effectID INTEGER, effectName TEXT, effectCategory INTEGER, modifierInfo TEXT)`,
		`CREATE TABLE dgmAttributeTypes (attributeID INTEGER, attributeName TEXT, defaultValue REAL, stackable INTEGER, highIsGood INTEGER)`,
		`CREATE TABLE invGroups (groupID INTEGER, categoryID INTEGER, groupName TEXT)`,
		`CREATE TABLE dgmTypeAttributes (typeID INTEGER, attributeID INTEGER, valueInt REAL, valueFloat REAL)`,
		`CREATE TABLE chrCloneGrades (cloneGradeID INTEGER, name TEXT)`,
		`CREATE TABLE chrCloneGradeSkills (cloneGradeID INTEGER, typeID INTEGER, level INTEGER)`,
		`INSERT INTO invTypes VALUES (34, 18, 'Tritanium', 1, 0, 0, 0), (587, 25, 'Rifter', 1, 0, 0, 0), (44992, 1, 'PLEX', 1, 0, 0, 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	require.NoError(t, db.Close())
	s, err := sde.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.True(t, s.Available())
	return s
}

func TestUnknownTypeIDNeverReachesESI(t *testing.T) {
	// ESI answers an unknown id with a 404, which spends the application's error limit; a
	// public caller inventing ids must be answered from the SDE.
	var calls atomic.Int32
	c, _ := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		calls.Add(1)
		writeJSON(w, 404, `{"error":"Type not found"}`, nil)
	})
	s := tinyTypesSDE(t)

	info, data, err := getTypeInfo(context.Background(), c, s, 999999999)
	require.NoError(t, err)
	require.Nil(t, data)
	require.Contains(t, info, "Unknown type_id=999999999. Do NOT invent a type_id.")

	price, mp, err := getMarketPrice(context.Background(), c, s, 999999999)
	require.NoError(t, err)
	require.Nil(t, mp)
	require.Contains(t, price, "Unknown type_id=999999999. Do NOT invent a type_id.")

	require.Zero(t, calls.Load(), "no request was sent for ids the SDE does not know")

	// A known id still goes upstream.
	c2, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 200, `{"name":"Rifter","description":"","volume":1,"mass":1,"group_id":25}`, nil)
	})
	got, _, err := getTypeInfo(context.Background(), c2, s, 587)
	require.NoError(t, err)
	require.Contains(t, got, "Name: Rifter")
	require.Len(t, f.requests(), 1)
}

func TestUpstreamBusyMarketPriceAndTypeInfoAreErrors(t *testing.T) {
	// A refused (not sent) ESI call is an error the data API can answer as upstream_busy,
	// for the two tools that used to turn every failure into a text.
	c, f := newFakeESIClient(t, func(w http.ResponseWriter, r *http.Request, rec esiRec) {
		writeJSON(w, 429, `{}`, map[string]string{"Retry-After": "600", "X-Ratelimit-Group": "g", "X-Ratelimit-Limit": "600/15m"})
	})
	e, err := esi.New(esi.Config{BaseURL: f.srv.URL, HTTPClient: f.srv.Client(), MaxWait: -1})
	require.NoError(t, err)
	c.ESI = e

	s := tinyTypesSDE(t)
	for name, call := range map[string]func() error{
		"market": func() error { _, _, err := getMarketPrice(context.Background(), c, s, 34); return err },
		"type":   func() error { _, _, err := getTypeInfo(context.Background(), c, s, 587); return err },
	} {
		t.Run(name, func(t *testing.T) {
			_ = call() // first call: the 429 itself (a message), arms the back-off
			err := call()
			require.ErrorIs(t, err, esi.ErrBackedOff)
			retry, busy := UpstreamBusy(err)
			require.True(t, busy)
			require.GreaterOrEqual(t, retry.Seconds(), 1.0)
		})
	}

	_, busy := UpstreamBusy(context.Canceled)
	require.False(t, busy)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = getTypeInfo(ctx, c, s, 587)
	require.Error(t, err)
	_, busy = UpstreamBusy(err)
	require.False(t, busy, "a call cut off by its own context is not upstream-busy")
}
