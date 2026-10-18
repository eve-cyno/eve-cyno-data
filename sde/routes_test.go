package sde

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func routeSDE(t *testing.T) *SDE {
	t.Helper()
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sysID(t *testing.T, s *SDE, name string) int {
	t.Helper()
	id := s.ResolveSystemName(name)
	require.NotNil(t, id, name)
	return *id
}

func TestGetJumpsPathOpts_ZeroOptionsMatchGetJumpsPath(t *testing.T) {
	s := routeSDE(t)
	jita, amarr := sysID(t, s, "Jita"), sysID(t, s, "Amarr")
	for _, pref := range []bool{false, true} {
		wantP, wantOK := s.GetJumpsPath(jita, amarr, pref)
		gotP, gotOK := s.GetJumpsPathOpts(jita, amarr, RouteOptions{PreferHighSec: pref})
		require.Equal(t, wantP, gotP)
		require.Equal(t, wantOK, gotOK)
	}
}

func TestGetJumpsPathOpts_Avoid(t *testing.T) {
	s := routeSDE(t)
	jita, amarr, ahb := sysID(t, s, "Jita"), sysID(t, s, "Amarr"), sysID(t, s, "Ahbazon")
	path, ok := s.GetJumpsPathOpts(jita, amarr, RouteOptions{Avoid: map[int]bool{ahb: true}})
	require.True(t, ok)
	require.Equal(t, 23, len(path)-1)
	require.NotContains(t, path, ahb)
}

func TestGetJumpsPathOpts_AvoidNeverBlocksEndpoints(t *testing.T) {
	s := routeSDE(t)
	jita, amarr := sysID(t, s, "Jita"), sysID(t, s, "Amarr")
	path, ok := s.GetJumpsPathOpts(jita, amarr, RouteOptions{Avoid: map[int]bool{jita: true, amarr: true}})
	require.True(t, ok)
	require.Equal(t, 11, len(path)-1)
}

func TestGetJumpsPathOpts_AvoidThatCutsEveryPath(t *testing.T) {
	s := routeSDE(t)
	jita, amarr := sysID(t, s, "Jita"), sysID(t, s, "Amarr")
	avoid := map[int]bool{}
	for _, n := range []string{"Ikuchi", "Maurasi", "Muvolailen", "New Caldari", "Niyabainen", "Perimeter", "Sobaseki"} {
		avoid[sysID(t, s, n)] = true
	}
	path, ok := s.GetJumpsPathOpts(jita, amarr, RouteOptions{Avoid: avoid})
	require.Nil(t, path)
	require.True(t, ok)
}

func TestGetJumpsPathOpts_PreferHighSecWithAvoidThatCutsEveryHighsecPath(t *testing.T) {
	s := routeSDE(t)
	jita, amarr := sysID(t, s, "Jita"), sysID(t, s, "Amarr")
	// One full layer of the highsec-only search (jump 5 from Jita) is avoided, except the
	// systems the shortest route uses: no highsec-only route exists any more, but the
	// shortest route still does.
	shortest, _ := s.GetJumpsPathOpts(jita, amarr, RouteOptions{})
	onShortest := map[int]bool{}
	for _, id := range shortest {
		onShortest[id] = true
	}
	avoid := map[int]bool{}
	for id, d := range s.JumpDistances(jita, true, nil) {
		if d == 5 && !onShortest[id] {
			avoid[id] = true
		}
	}
	require.NotEmpty(t, avoid)
	path, ok := s.GetJumpsPathOpts(jita, amarr, RouteOptions{PreferHighSec: true, Avoid: avoid})
	require.NotNil(t, path)
	require.False(t, ok)
	for _, id := range path {
		if id != amarr {
			require.False(t, avoid[id])
		}
	}
}

func TestJumpDistances(t *testing.T) {
	s := routeSDE(t)
	jita, amarr := sysID(t, s, "Jita"), sysID(t, s, "Amarr")
	d := s.JumpDistances(jita, false, nil)
	_, hasOrigin := d[jita]
	require.True(t, hasOrigin)
	// the origin is exempt from avoid
	require.Equal(t, 0, s.JumpDistances(jita, false, map[int]bool{jita: true})[jita])
	require.Equal(t, 11, s.JumpDistances(jita, false, map[int]bool{jita: true})[amarr])
	require.Equal(t, 1, d[sysID(t, s, "Perimeter")])
	require.Equal(t, 11, d[amarr])
	require.Equal(t, 12, d[sysID(t, s, "Dodixie")])
	_, ok := d[sysID(t, s, "Thera")]
	require.False(t, ok)
	require.Equal(t, 34, s.JumpDistances(jita, true, nil)[amarr])
	require.Equal(t, 23, s.JumpDistances(jita, false, map[int]bool{sysID(t, s, "Ahbazon"): true})[amarr])
}

func TestJumpDistancesExempt(t *testing.T) {
	s := routeSDE(t)
	jita, amarr, ahb := sysID(t, s, "Jita"), sysID(t, s, "Amarr"), sysID(t, s, "Ahbazon")
	avoid := map[int]bool{ahb: true}
	// an exempt avoided system is measured but never expanded through
	d := s.JumpDistancesExempt(jita, false, avoid, map[int]bool{ahb: true})
	require.Equal(t, 4, d[ahb])
	require.Equal(t, 23, d[amarr])
	// not exempt: not reached at all
	d = s.JumpDistancesExempt(jita, false, avoid, nil)
	_, ok := d[ahb]
	require.False(t, ok)
	require.Equal(t, 23, d[amarr])
	// the delegating form is unchanged
	require.Equal(t, s.JumpDistances(jita, false, avoid), s.JumpDistancesExempt(jita, false, avoid, nil))
}

func TestResolveSystemNames_CaseDuplicatesAndUnknown(t *testing.T) {
	s := routeSDE(t)
	ids, names, bad := s.ResolveSystemNames([]string{"jita", "JITA", " Amarr ", "Nope99", "nope99", ""})
	require.Equal(t, []string{"Jita", "Amarr"}, names)
	require.Len(t, ids, 2)
	require.Equal(t, []string{"Nope99"}, bad)
}

func TestResolveSystemNames_ClosedSDEReportsEveryName(t *testing.T) {
	s, err := Open(dbPath(t))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	ids, names, bad := s.ResolveSystemNames([]string{"Jita", " jita ", "Amarr", ""})
	require.Empty(t, ids)
	require.Empty(t, names)
	require.Equal(t, []string{"Jita", "Amarr"}, bad)
}

func TestSystemSecurity(t *testing.T) {
	s := routeSDE(t)
	sec, ok := s.SystemSecurity(sysID(t, s, "Jita"))
	require.True(t, ok)
	require.InDelta(t, 0.9459, sec, 0.001)
	_, ok = s.SystemSecurity(-5)
	require.False(t, ok)
}

func TestFactionName(t *testing.T) {
	s := routeSDE(t)
	require.Equal(t, "Caldari State", s.FactionName(500001))
	require.Equal(t, "CONCORD Assembly", s.FactionName(500006))
	require.Equal(t, "", s.FactionName(1))
}
