package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"eve-cyno.dev/go/data/sde"
	"eve-cyno.dev/go/data/sde/sdetest"
	"github.com/stretchr/testify/require"
)

// TestBattleFlagToSlot mirrors the Python _BATTLE_SLOT_RANGES table.
func TestBattleFlagToSlot(t *testing.T) {
	cases := []struct {
		flag int
		want string
	}{
		// High slots 27–34
		{27, "High slots"},
		{30, "High slots"},
		{34, "High slots"},
		// Mid slots 19–26
		{19, "Mid slots"},
		{22, "Mid slots"},
		{26, "Mid slots"},
		// Low slots 11–18
		{11, "Low slots"},
		{15, "Low slots"},
		{18, "Low slots"},
		// Rigs 92–94
		{92, "Rigs"},
		{93, "Rigs"},
		{94, "Rigs"},
		// Subsystems 125–128
		{125, "Subsystems"},
		{128, "Subsystems"},
		// Outside ranges (cargo, drone bay, etc.) → ""
		{0, ""},
		{5, ""},
		{35, ""},  // just above High
		{91, ""},  // just below Rigs
		{95, ""},  // just above Rigs
		{124, ""}, // just below Subsystems
		{129, ""}, // just above Subsystems
	}
	for _, tc := range cases {
		got := battleFlagToSlot(tc.flag)
		if got != tc.want {
			t.Errorf("battleFlagToSlot(%d) = %q; want %q", tc.flag, got, tc.want)
		}
	}
}

// TestAggregateDamageByWeapon verifies the weapon aggregation logic.
func TestAggregateDamageByWeapon(t *testing.T) {
	killmails := []Killmail{
		{
			KillmailID: 1,
			Attackers: []KillmailAttacker{
				{WeaponTypeID: 100, DamageDone: 5000},
				{WeaponTypeID: 200, DamageDone: 3000},
				{WeaponTypeID: 100, DamageDone: 2000}, // same weapon, same km — kill_count = 1
			},
		},
		{
			KillmailID: 2,
			Attackers: []KillmailAttacker{
				{WeaponTypeID: 100, DamageDone: 4000},
			},
		},
		{
			KillmailID: 3,
			Attackers: []KillmailAttacker{
				{WeaponTypeID: 300, DamageDone: 1000},
			},
		},
	}
	sideLookup := map[int]string{
		1: "Side A",
		2: "Side A",
		3: "Unknown", // no entry → bucketed as "Unknown"
	}

	result := aggregateDamageByWeapon(killmails, sideLookup)

	// Side A: weapon 100 = 5000+2000+4000=11000 dmg, 2 kills; weapon 200 = 3000, 1 kill
	sideA, ok := result["Side A"]
	if !ok {
		t.Fatal("expected 'Side A' in result")
	}
	if len(sideA) != 2 {
		t.Fatalf("Side A: expected 2 entries, got %d", len(sideA))
	}
	// First entry must be weapon 100 (highest damage)
	if sideA[0].WeaponTypeID != 100 {
		t.Errorf("Side A[0].WeaponTypeID = %d; want 100", sideA[0].WeaponTypeID)
	}
	if sideA[0].TotalDamage != 11000 {
		t.Errorf("Side A[0].TotalDamage = %d; want 11000", sideA[0].TotalDamage)
	}
	if sideA[0].KillCount != 2 {
		t.Errorf("Side A[0].KillCount = %d; want 2", sideA[0].KillCount)
	}
	if sideA[1].WeaponTypeID != 200 {
		t.Errorf("Side A[1].WeaponTypeID = %d; want 200", sideA[1].WeaponTypeID)
	}
	if sideA[1].TotalDamage != 3000 {
		t.Errorf("Side A[1].TotalDamage = %d; want 3000", sideA[1].TotalDamage)
	}
	if sideA[1].KillCount != 1 {
		t.Errorf("Side A[1].KillCount = %d; want 1", sideA[1].KillCount)
	}

	// Unknown side: weapon 300
	unk, ok := result["Unknown"]
	if !ok {
		t.Fatal("expected 'Unknown' in result")
	}
	if len(unk) != 1 || unk[0].WeaponTypeID != 300 {
		t.Errorf("Unknown side unexpected: %+v", unk)
	}

	// Zero weapon or zero damage are skipped
	kmWithZero := []Killmail{
		{
			KillmailID: 10,
			Attackers: []KillmailAttacker{
				{WeaponTypeID: 0, DamageDone: 5000}, // wid=0 → skip
				{WeaponTypeID: 500, DamageDone: 0},  // dmg=0 → skip
			},
		},
	}
	r2 := aggregateDamageByWeapon(kmWithZero, map[int]string{10: "S1"})
	if s1, ok := r2["S1"]; ok && len(s1) > 0 {
		t.Errorf("expected no entries for zero-wid/zero-dmg, got %+v", s1)
	}
}

// TestAggregateDestroyedModules verifies the module aggregation logic.
func TestAggregateDestroyedModules(t *testing.T) {
	killmails := []Killmail{
		{
			KillmailID: 1,
			Victim: KillmailVictim{
				Items: []KillmailVictimItem{
					{Flag: 27, ItemTypeID: 400, QuantityDropped: 1, QuantityDestroyed: 0}, // High slot
					{Flag: 19, ItemTypeID: 500, QuantityDropped: 0, QuantityDestroyed: 2}, // Mid slot
					{Flag: 5, ItemTypeID: 600, QuantityDropped: 1, QuantityDestroyed: 0},  // cargo → skip
				},
			},
		},
		{
			KillmailID: 2,
			Victim: KillmailVictim{
				Items: []KillmailVictimItem{
					{Flag: 27, ItemTypeID: 400, QuantityDropped: 0, QuantityDestroyed: 0}, // qty=0 → qty=1
					{Flag: 0, ItemTypeID: 700, QuantityDropped: 1, QuantityDestroyed: 0},  // flag=0 → skip
				},
			},
		},
	}
	sideLookup := map[int]string{1: "Side A", 2: "Side A"}

	result := aggregateDestroyedModules(killmails, sideLookup)
	sideA, ok := result["Side A"]
	if !ok {
		t.Fatal("expected 'Side A' in result")
	}

	// High slots: typeID 400, count = 1+1=2
	hi, ok := sideA["High slots"]
	if !ok || len(hi) != 1 {
		t.Fatalf("High slots: expected 1 entry, got %+v", hi)
	}
	if hi[0].TypeID != 400 || hi[0].Count != 2 {
		t.Errorf("High slots[0] = %+v; want {TypeID:400 Count:2}", hi[0])
	}

	// Mid slots: typeID 500, count = 2
	mi, ok := sideA["Mid slots"]
	if !ok || len(mi) != 1 {
		t.Fatalf("Mid slots: expected 1 entry, got %+v", mi)
	}
	if mi[0].TypeID != 500 || mi[0].Count != 2 {
		t.Errorf("Mid slots[0] = %+v; want {TypeID:500 Count:2}", mi[0])
	}

	// No entry for flag=5 (cargo) or flag=0
	if _, exists := sideA["Cargo"]; exists {
		t.Error("unexpected 'Cargo' slot")
	}
}

// TestFormatBattleBreakdown does a byte-exact match of the output format.
// Uses nil SDE so all names fall back to "Type#N".
func TestFormatBattleBreakdown(t *testing.T) {
	aggDmg := map[string][]WeaponEntry{
		"Side A": {{WeaponTypeID: 100, TotalDamage: 11000, KillCount: 2}},
		"Side B": {{WeaponTypeID: 200, TotalDamage: 3000, KillCount: 1}},
	}
	aggMods := map[string]map[string][]ModuleEntry{
		"Side A": {
			"High slots": {{TypeID: 400, Count: 3}},
			"Mid slots":  {{TypeID: 500, Count: 1}},
		},
	}

	got := formatBattleBreakdown(nil, aggDmg, aggMods)

	// Check the exact line formats that eval parity depends on.
	wantLines := []string{
		"",
		"### Damage by weapon type",
		"**Side A** — top weapons by damage dealt:",
		"  • Type#100 — 11,000 dmg across 2 kill(s)",
		"**Side B** — top weapons by damage dealt:",
		"  • Type#200 — 3,000 dmg across 1 kill(s)",
		"",
		"### Modules destroyed by slot",
		"**Side A** — most destroyed modules:",
		"  High slots:",
		"    • Type#400 ×3",
		"  Mid slots:",
		"    • Type#500 ×1",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want) {
			t.Errorf("output missing line:\n  want: %q\n  got:\n%s", want, got)
		}
	}

	// Empty aggDmg / aggMods should produce the sentinel lines.
	empty := formatBattleBreakdown(nil, nil, nil)
	if !strings.Contains(empty, "  (no killmails resolved)") {
		t.Errorf("empty result missing sentinel; got:\n%s", empty)
	}
}

// TestFmtThousands validates Python-compatible {:,} formatting.
func TestFmtThousands(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{-5000, "-5,000"},
	}
	for _, tc := range cases {
		if got := fmtThousands(tc.n); got != tc.want {
			t.Errorf("fmtThousands(%d) = %q; want %q", tc.n, got, tc.want)
		}
	}
}

// TestResolveSystemNameFromKillmails is skipped when no sde.sqlite is present
// (CI without the full SDE file). When the DB is available it verifies that a
// known solar_system_id (30000142 = Jita) resolves to its canonical name.
func TestResolveSystemNameFromKillmails(t *testing.T) {
	dbPath := sdetest.Path(t)
	s, err := sde.Open(dbPath)
	if err != nil {
		sdetest.Skipf(t, "could not open sde.sqlite: %v", err)
	}
	defer s.Close()

	kms := []Killmail{
		{KillmailID: 99, SolarSystemID: 30000142}, // Jita
	}
	got := resolveSystemNameFromKillmails(s, kms)
	if got == "" {
		t.Error("resolveSystemNameFromKillmails: expected non-empty name for Jita (30000142)")
	}
	if !strings.EqualFold(got, "Jita") {
		t.Errorf("resolveSystemNameFromKillmails(30000142) = %q; want 'Jita'", got)
	}

	// Empty input → ""
	if got := resolveSystemNameFromKillmails(s, nil); got != "" {
		t.Errorf("empty killmails should return empty, got %q", got)
	}

	// Unknown system ID → ""
	unknown := []Killmail{{KillmailID: 1, SolarSystemID: 999999999}}
	if got := resolveSystemNameFromKillmails(s, unknown); got != "" {
		t.Errorf("unknown system should return empty, got %q", got)
	}
}

// ── zkill related-API-degraded → br.evetools.org fallback ─────────────────
//
// zkillboard's /api/related/{system}/{time}/ endpoint is known to degrade
// under load, returning HTTP 200 with e.g. {"complete": false} and no
// "summary" key instead of real battle data. analyzeZKill must detect that
// specific "no data" shape and fall back to analyzeEveToolsRelated rather
// than surface a "no data" message for an outage that isn't zKillboard's
// alone to own.

// fakeHostRT is a minimal http.RoundTripper that routes canned responses by
// request hostname, so a single test can fake multiple upstream hosts
// (zkillboard.com, br.evetools.org, esi.evetech.net) without touching the
// network. Unrouted hosts get a harmless 404 — callers in this package all
// tolerate non-200 responses as recoverable errors.
type fakeHostRT struct {
	byHost map[string]fakeHostResponse
}

type fakeHostResponse struct {
	status int
	body   string
}

func (f *fakeHostRT) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, ok := f.byHost[r.URL.Host]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	}
	status := resp.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(resp.body)), Header: make(http.Header)}, nil
}

const zkillRelatedIncompleteBody = `{"complete": false}`

const zkillNoDataMessage = "Unexpected response from zKillboard API."

const eveToolsRelatedFakeBody = `{
  "result": "success",
  "datetime": "202504280000",
  "systemID": 30003068,
  "system": {"id": 30003068, "name": "Kourmonen", "region": "The Bleak Lands", "regionId": 10000038, "ss": "0.4"},
  "viewed": 0,
  "kms": [
    {
      "id": 126640609,
      "hash": "a6c7c19501a4f054f05c58a85920b0737602c71b",
      "system": 30003068,
      "time": 1745796151000,
      "totalValue": 10000000,
      "victim": {"ally": 0, "corp": 98108802, "char": 91737150, "ship": 670, "lossValue": 10000000, "dmg": 461, "fctn": 0},
      "attackers": [
        {"ally": 99013563, "corp": 98767024, "char": 2116231052, "ship": 78333, "weap": 2977, "dmg": 461, "fctn": 0}
      ]
    },
    {
      "id": 126640649,
      "hash": "996ff8c64e3a346b83f9f775d4fc4767b1cbe02c",
      "system": 30003068,
      "time": 1745796267000,
      "totalValue": 18835659,
      "victim": {"ally": 0, "corp": 98108802, "char": 91737151, "ship": 32872, "lossValue": 18835659, "dmg": 5267, "fctn": 0},
      "attackers": [
        {"ally": 99013563, "corp": 98767024, "char": 2116231052, "ship": 78333, "weap": 2977, "dmg": 5267, "fctn": 0}
      ]
    }
  ]
}`

// TestAnalyzeZKillFallsBackToEveTools pins the happy-path fallback: zKill's
// related endpoint returns its known "incomplete" shape, and br.evetools.org
// has the same battle window — the result must carry real battle content
// (not the zKill "no data" sentinel).
func TestAnalyzeZKillFallsBackToEveTools(t *testing.T) {
	rt := &fakeHostRT{byHost: map[string]fakeHostResponse{
		"zkillboard.com":  {status: 200, body: zkillRelatedIncompleteBody},
		"br.evetools.org": {status: 200, body: eveToolsRelatedFakeBody},
	}}
	c := WithTransport(rt)

	got, err := analyzeZKill(context.Background(), c, nil,
		"https://zkillboard.com/related/30003068/202504280000/", "summary")
	require.NoError(t, err)

	require.NotContains(t, got, zkillNoDataMessage, "should not surface zkill's no-data message when evetools fallback succeeds")
	require.Contains(t, got, "Kourmonen", "expected system name from the evetools fallback")
	require.Contains(t, got, "Battle Report", "expected a rendered battle report, not an error string")
}

// TestAnalyzeZKillFallbackBothFail pins today's behavior when BOTH backends
// fail: the result must equal zKill's original "no data" message — never
// worse than before this fallback existed.
func TestAnalyzeZKillFallbackBothFail(t *testing.T) {
	rt := &fakeHostRT{byHost: map[string]fakeHostResponse{
		"zkillboard.com":  {status: 200, body: zkillRelatedIncompleteBody},
		"br.evetools.org": {status: 500, body: "internal error"},
	}}
	c := WithTransport(rt)

	got, err := analyzeZKill(context.Background(), c, nil,
		"https://zkillboard.com/related/30003068/202504280000/", "summary")
	require.NoError(t, err)
	require.Equal(t, zkillNoDataMessage, got, "both backends failing must pin to zkill's original no-data message")
}

// TestAnalyzeZKillGenuineErrorsUnaffected verifies the fallback is scoped to
// the "no data / incomplete" shape only — a genuinely unparseable zKill
// response keeps today's behavior with no evetools fallback attempted. If a
// fallback fired here, it would return evetools' real battle content instead
// of the pinned no-data message, revealing an over-broad trigger condition.
func TestAnalyzeZKillGenuineErrorsUnaffected(t *testing.T) {
	rt := &fakeHostRT{byHost: map[string]fakeHostResponse{
		"zkillboard.com":  {status: 200, body: "not json at all"},
		"br.evetools.org": {status: 200, body: eveToolsRelatedFakeBody},
	}}
	c := WithTransport(rt)

	got, err := analyzeZKill(context.Background(), c, nil,
		"https://zkillboard.com/related/30003068/202504280000/", "summary")
	require.NoError(t, err)
	require.Equal(t, zkillNoDataMessage, got, "unparseable zkill response must not trigger the evetools fallback")
}
