package sde

import (
	"container/list"
)

// ResolveSystemName case-insensitively maps a system name to its solarSystemID.
func (s *SDE) ResolveSystemName(name string) *int {
	if !s.HasTable("mapSolarSystems") {
		return nil
	}
	var id int
	err := s.db.QueryRow(
		"SELECT solarSystemID FROM mapSolarSystems WHERE solarSystemName = ? COLLATE NOCASE",
		name).Scan(&id)
	if err != nil {
		return nil
	}
	return &id
}

// ResolveRegionName case-insensitively maps a region name to its regionID.
func (s *SDE) ResolveRegionName(name string) *int {
	if !s.HasTable("mapRegions") {
		return nil
	}
	var id int
	err := s.db.QueryRow(
		"SELECT regionID FROM mapRegions WHERE regionName = ? COLLATE NOCASE",
		name).Scan(&id)
	if err != nil {
		return nil
	}
	return &id
}

// GetSystemName resolves a solarSystemID to its name, or nil if unknown.
func (s *SDE) GetSystemName(solarSystemID int) *string {
	if !s.HasTable("mapSolarSystems") {
		return nil
	}
	var name string
	err := s.db.QueryRow(
		"SELECT solarSystemName FROM mapSolarSystems WHERE solarSystemID = ?",
		solarSystemID).Scan(&name)
	if err != nil {
		return nil
	}
	return &name
}

// GetSystemMetadata returns a full metadata dict for a solarSystemID.
func (s *SDE) GetSystemMetadata(systemID int) map[string]any {
	if !s.HasTable("mapSolarSystems") {
		return nil
	}
	var sysID, constellID, regionID int
	var sysName, constellName, regionName, secClass *string
	var security *float64
	var factionID *int
	err := s.db.QueryRow(`
		SELECT s.solarSystemID, s.solarSystemName, s.security, s.securityClass,
		       s.regionID, r.regionName, s.constellationID, c.constellationName,
		       s.factionID
		FROM mapSolarSystems s
		LEFT JOIN mapRegions r ON s.regionID = r.regionID
		LEFT JOIN mapConstellations c ON s.constellationID = c.constellationID
		WHERE s.solarSystemID = ?`, systemID).Scan(
		&sysID, &sysName, &security, &secClass,
		&regionID, &regionName, &constellID, &constellName,
		&factionID)
	if err != nil {
		return nil
	}
	return map[string]any{
		"system_id":          sysID,
		"system_name":        derefStr(sysName),
		"security":           derefFloat(security),
		"security_class":     derefStr(secClass),
		"region_id":          regionID,
		"region_name":        derefStr(regionName),
		"constellation_id":   constellID,
		"constellation_name": derefStr(constellName),
		"faction_id":         derefIntPtr(factionID),
	}
}

// GetSystemsInRegion returns systems in a region filtered by security range.
func (s *SDE) GetSystemsInRegion(regionID int, secMin, secMax float64) []map[string]any {
	if !s.HasTable("mapSolarSystems") {
		return nil
	}
	rows, err := s.db.Query(`
		SELECT solarSystemID, solarSystemName, security FROM mapSolarSystems
		WHERE regionID = ? AND security >= ? AND security <= ?
		ORDER BY security DESC, solarSystemName ASC`, regionID, secMin, secMax)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []map[string]any
	for rows.Next() {
		var sysID int
		var name string
		var sec *float64
		if err := rows.Scan(&sysID, &name, &sec); err != nil {
			continue
		}
		result = append(result, map[string]any{
			"system_id": sysID,
			"name":      name,
			"security":  derefFloat(sec),
		})
	}
	return result
}

// mapCaches holds the lazily-built adjacency and security maps. gen is the
// db generation they were built from; a hot-reloaded SDE file bumps it and the
// cache is rebuilt on next use.
type mapCaches struct {
	gen uint64
	adj map[int][]int
	sec map[int]float64
}

func (s *SDE) ensureMapCaches() *mapCaches {
	// acquire() first: it is where a due reload happens, so gen is current, and it
	// pins this generation until the cache is built.
	h, err := s.db.acquire()
	if err != nil {
		return &mapCaches{adj: map[int][]int{}, sec: map[int]float64{}} // closed: empty, not cached
	}
	defer h.release()
	s.mapMu.Lock()
	defer s.mapMu.Unlock()
	if s.mapCache != nil && s.mapCache.gen == h.gen {
		return s.mapCache
	}
	adj := make(map[int][]int)
	sec := make(map[int]float64)
	rows, err := h.db.Query("SELECT fromSolarSystemID, toSolarSystemID FROM mapSolarSystemJumps")
	if err == nil {
		for rows.Next() {
			var from, to int
			if err := rows.Scan(&from, &to); err == nil {
				adj[from] = append(adj[from], to)
			}
		}
		rows.Close()
	}
	rows2, err := h.db.Query("SELECT solarSystemID, security FROM mapSolarSystems")
	if err == nil {
		for rows2.Next() {
			var sysID int
			var security *float64
			if err := rows2.Scan(&sysID, &security); err == nil {
				if security != nil {
					sec[sysID] = *security
				} else {
					sec[sysID] = 0.0
				}
			}
		}
		rows2.Close()
	}
	s.mapCache = &mapCaches{gen: h.gen, adj: adj, sec: sec}
	return s.mapCache
}

// GetJumpsPath runs BFS over mapSolarSystemJumps.
// Returns (path []int or nil, constraintSatisfied bool).
// [OPUS-REVIEW] if BFS tie-breaking order diverges from Python golden.
func (s *SDE) GetJumpsPath(fromSystemID, toSystemID int, preferHighSec bool) ([]int, bool) {
	if !s.HasTable("mapSolarSystemJumps") {
		return nil, true
	}
	if fromSystemID == toSystemID {
		return []int{fromSystemID}, true
	}
	mc := s.ensureMapCaches()

	bfs := func(highsecOnly bool) []int {
		visited := map[int]bool{fromSystemID: true}
		parents := map[int]int{}
		queue := list.New()
		queue.PushBack(fromSystemID)
		for queue.Len() > 0 {
			cur := queue.Front().Value.(int)
			queue.Remove(queue.Front())
			if cur == toSystemID {
				path := []int{cur}
				for {
					p, ok := parents[path[len(path)-1]]
					if !ok {
						break
					}
					path = append(path, p)
				}
				// reverse
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return path
			}
			neighbours := mc.adj[cur]
			if highsecOnly {
				// sort by descending security (mirrors Python sorted(neighbours, key=lambda n: -sec_lookup.get(n, 0.0)))
				sorted := make([]int, len(neighbours))
				copy(sorted, neighbours)
				sortBySecDesc(sorted, mc.sec)
				neighbours = sorted
			}
			for _, nb := range neighbours {
				if visited[nb] {
					continue
				}
				if highsecOnly && mc.sec[nb] < 0.45 {
					continue
				}
				visited[nb] = true
				parents[nb] = cur
				queue.PushBack(nb)
			}
		}
		return nil
	}

	if preferHighSec {
		path := bfs(true)
		if path != nil {
			return path, true
		}
		return bfs(false), false
	}
	return bfs(false), true
}

// sortBySecDesc sorts system IDs by descending security status (stable by original order for ties).
func sortBySecDesc(ids []int, sec map[int]float64) {
	// insertion sort — small lists, preserves tie order like Python's sort(key=...)
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && sec[ids[j]] > sec[ids[j-1]]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

func derefStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefIntPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
