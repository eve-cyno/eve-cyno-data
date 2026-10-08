package sde

import (
	"sort"
	"strings"
)

// GetTypeName returns typeName for typeID, or nil if not found.
func (s *SDE) GetTypeName(typeID int) *string {
	var name string
	err := s.db.QueryRow(`SELECT typeName FROM invTypes WHERE typeID=?`, typeID).Scan(&name)
	if err != nil {
		return nil
	}
	return &name
}

// GetTypeNames returns {typeID: typeName} for a batch of type IDs.
func (s *SDE) GetTypeNames(typeIDs []int) map[int]string {
	if len(typeIDs) == 0 {
		return map[int]string{}
	}
	placeholders := strings.Repeat("?,", len(typeIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(typeIDs))
	for i, id := range typeIDs {
		args[i] = id
	}
	rows, err := s.db.Query(
		"SELECT typeID, typeName FROM invTypes WHERE typeID IN ("+placeholders+")", args...)
	if err != nil {
		return map[int]string{}
	}
	defer rows.Close()
	result := make(map[int]string, len(typeIDs))
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			result[id] = name
		}
	}
	return result
}

// ResolveNames returns {inputName: typeID} for published types matching any of the given names.
// Matching is case-insensitive (COLLATE NOCASE) so "tritanium" resolves the same as "Tritanium".
// The result map is keyed by the original input name, not the canonical DB spelling.
func (s *SDE) ResolveNames(names []string) map[string]int {
	if len(names) == 0 {
		return map[string]int{}
	}
	placeholders := strings.Repeat("?,", len(names))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = n
	}
	rows, err := s.db.Query(
		"SELECT typeName, typeID, published FROM invTypes WHERE typeName COLLATE NOCASE IN ("+placeholders+") "+
			"ORDER BY published DESC, typeID ASC", args...)
	if err != nil {
		return map[string]int{}
	}
	defer rows.Close()
	// Build lowercase(typeName) → typeID from DB rows.
	byLower := make(map[string]int)
	for rows.Next() {
		var typeName string
		var typeID, published int
		if err := rows.Scan(&typeName, &typeID, &published); err != nil {
			continue
		}
		low := strings.ToLower(typeName)
		if _, exists := byLower[low]; !exists { // prefer published=1 (ORDER BY DESC already ensures this)
			byLower[low] = typeID
		}
	}
	// Re-key by original input name so callers can do ids[inputName].
	result := make(map[string]int, len(byLower))
	for _, n := range names {
		if tid, ok := byLower[strings.ToLower(n)]; ok {
			result[n] = tid
		}
	}
	return result
}

// ResolveNameLoose tries the full name; if no SDE match, progressively drops trailing words.
func (s *SDE) ResolveNameLoose(name string, maxDrop int) *[2]any {
	parts := strings.Fields(strings.TrimSpace(name))
	if len(parts) == 0 {
		return nil
	}
	if maxDrop <= 0 {
		maxDrop = 5
	}
	cap := maxDrop
	if len(parts)-1 < cap {
		cap = len(parts) - 1
	}
	for drop := 0; drop <= cap; drop++ {
		keep := len(parts) - drop
		if keep <= 0 {
			break
		}
		candidate := strings.Join(parts[:keep], " ")
		ids := s.ResolveNames([]string{candidate})
		if tid, ok := ids[candidate]; ok {
			result := [2]any{candidate, tid}
			return &result
		}
	}
	return nil
}

// FuzzyMatch suggests the closest typeName matches for a possibly-misspelled item.
// Uses the same scoring as Python's difflib.SequenceMatcher(None, a, b).ratio().
// [OPUS-REVIEW] if ratio ordering diverges from Python golden.
func (s *SDE) FuzzyMatch(name string, limit int, cutoff float64) [][2]any {
	if name == "" {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	if cutoff == 0 {
		cutoff = 0.6
	}
	prefix := name
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	rows, err := s.db.Query(
		"SELECT typeID, typeName FROM invTypes "+
			"WHERE typeName LIKE ? AND published=1 "+
			"ORDER BY typeID ASC LIMIT 500",
		prefix+"%")
	if err != nil {
		return nil
	}
	defer rows.Close()

	type scored struct {
		id    int
		name  string
		score float64
	}
	nameLower := strings.ToLower(name)
	var candidates []scored
	for rows.Next() {
		var id int
		var tname string
		if err := rows.Scan(&id, &tname); err != nil {
			continue
		}
		r := sequencerRatio(nameLower, strings.ToLower(tname))
		if r >= cutoff {
			candidates = append(candidates, scored{id, tname, r})
		}
	}
	// Sort: high score first, then shorter name (matches Python: key=lambda t: (-t[2], len(t[1])))
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return len(candidates[i].name) < len(candidates[j].name)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	result := make([][2]any, len(candidates))
	for i, c := range candidates {
		result[i] = [2]any{c.id, c.name}
	}
	return result
}

// SuggestCanonical suggests canonical SDE typeNames for a likely-fabricated module name.
func (s *SDE) SuggestCanonical(name string, limit int) [][2]any {
	if limit <= 0 {
		limit = 5
	}
	clean := strings.TrimSpace(name)
	if clean == "" {
		return nil
	}

	// 1. Exact match
	row := s.db.QueryRow(
		"SELECT typeID, typeName FROM invTypes WHERE typeName = ? AND published=1 LIMIT 1", clean)
	var exactID int
	var exactName string
	if err := row.Scan(&exactID, &exactName); err == nil {
		return [][2]any{{exactID, exactName}}
	}

	var results [][2]any
	seenIDs := make(map[int]bool)

	push := func(candidates [][2]any) {
		for _, c := range candidates {
			id := c[0].(int)
			tname := c[1].(string)
			low := strings.ToLower(tname)
			if strings.HasSuffix(low, " blueprint") || strings.Contains(low, " skin") {
				continue
			}
			if strings.Contains(low, "specialization") || strings.Contains(low, "operation") {
				continue
			}
			if seenIDs[id] {
				continue
			}
			results = append(results, [2]any{id, tname})
			seenIDs[id] = true
		}
	}

	// 2. Substring search on the semantic core (strip leading size prefix and trailing roman tier)
	tokens := strings.Fields(clean)
	var core string
	if len(tokens) >= 2 {
		trimmed := make([]string, len(tokens))
		copy(trimmed, tokens)
		for len(trimmed) > 0 && romanTier(trimmed[len(trimmed)-1]) {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if len(trimmed) > 0 && strings.ToLower(trimmed[len(trimmed)-1]) == "charge" {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if len(trimmed) >= 2 {
			core = strings.Join(trimmed[1:], " ")
		}
	}
	if core != "" {
		rows, err := s.db.Query(
			"SELECT typeID, typeName FROM invTypes "+
				"WHERE typeName LIKE ? AND published=1 "+
				"ORDER BY LENGTH(typeName), typeID LIMIT 30",
			"%"+core+"%")
		if err == nil {
			var batch [][2]any
			for rows.Next() {
				var id int
				var tname string
				if err := rows.Scan(&id, &tname); err == nil {
					batch = append(batch, [2]any{id, tname})
				}
			}
			rows.Close()
			push(batch)
		}
	}

	// 3. Launcher-name-as-ammo recovery
	if chargeRE(clean) {
		head := stripChargeSuffix(clean)
		var launcherID int
		if head != "" {
			// Exact name first
			r := s.db.QueryRow(
				"SELECT typeID FROM invTypes WHERE typeName=? AND published=1 LIMIT 1", head)
			_ = r.Scan(&launcherID) // no row (or an error) leaves launcherID at 0: try the variants
			if launcherID == 0 {
				for _, variant := range []string{head + " Launcher I", head + " Launcher II", head + " Launcher"} {
					r2 := s.db.QueryRow(
						"SELECT typeID FROM invTypes WHERE typeName=? AND published=1 LIMIT 1", variant)
					_ = r2.Scan(&launcherID)
					if launcherID != 0 {
						break
					}
				}
			}
		}
		if launcherID != 0 {
			var ammoGroup int
			_ = s.db.QueryRow(
				"SELECT COALESCE(valueInt, valueFloat) FROM dgmTypeAttributes WHERE typeID=? AND attributeID=604",
				launcherID).Scan(&ammoGroup)
			if ammoGroup != 0 {
				rows, err := s.db.Query(
					"SELECT typeID, typeName FROM invTypes "+
						"WHERE groupID=? AND published=1 "+
						"ORDER BY LENGTH(typeName), typeID LIMIT ?",
					ammoGroup, limit*2)
				if err == nil {
					var batch [][2]any
					for rows.Next() {
						var id int
						var tname string
						if err := rows.Scan(&id, &tname); err == nil {
							batch = append(batch, [2]any{id, tname})
						}
					}
					rows.Close()
					push(batch)
				}
			}
		}
	}

	// 4. Fallback to fuzzy
	if len(results) == 0 {
		push(s.FuzzyMatch(clean, limit*2, 0.6))
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// romanTier returns true for Roman tier suffix tokens (I, II, III, IV, V).
func romanTier(s string) bool {
	switch s {
	case "I", "II", "III", "IV", "V":
		return true
	}
	return false
}

// chargeRE returns true when name ends with "Charge" or "Charge I/II/III".
func chargeRE(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "charge" || strings.HasSuffix(lower, " charge") {
		return true
	}
	// "charge i", "charge ii", "charge iii"
	for _, suffix := range []string{" charge i", " charge ii", " charge iii"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// stripChargeSuffix removes "Charge" and optional tier suffix from the end.
func stripChargeSuffix(name string) string {
	s := strings.TrimSpace(name)
	// Try stripping " Charge I/II/III" etc
	for _, suffix := range []string{" Charge III", " Charge II", " Charge I", " Charge"} {
		if strings.HasSuffix(s, suffix) {
			return strings.TrimSpace(s[:len(s)-len(suffix)])
		}
	}
	return s
}

// sequencerRatio implements Python difflib.SequenceMatcher(None, a, b).ratio()
// using the Ratcliff/Obershelp (Gestalt Pattern Matching) algorithm.
// // regexp2: RE2 can't express this (not regex-related, but algorithmic parity note)
func sequencerRatio(a, b string) float64 {
	ra := []rune(a)
	rb := []rune(b)
	t := len(ra) + len(rb)
	if t == 0 {
		return 1.0
	}
	m := countMatchingRunes(ra, rb)
	return 2.0 * float64(m) / float64(t)
}

// countMatchingRunes computes total matching runes using the Ratcliff/Obershelp recursive method.
func countMatchingRunes(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	si, sj, sz := longestMatchRunes(a, b, 0, len(a), 0, len(b))
	if sz == 0 {
		return 0
	}
	total := sz
	if si > 0 && sj > 0 {
		total += countMatchingRunes(a[:si], b[:sj])
	}
	if si+sz < len(a) && sj+sz < len(b) {
		total += countMatchingRunes(a[si+sz:], b[sj+sz:])
	}
	return total
}

// longestMatchRunes finds the longest matching block in a[alo:ahi] and b[blo:bhi].
// Mirrors Python SequenceMatcher.find_longest_match using the j2len DP approach.
func longestMatchRunes(a, b []rune, alo, ahi, blo, bhi int) (int, int, int) {
	bestI, bestJ, bestSize := alo, blo, 0
	j2len := make(map[int]int, bhi-blo)
	for i := alo; i < ahi; i++ {
		newj2len := make(map[int]int, len(j2len)+1)
		for j := blo; j < bhi; j++ {
			if a[i] != b[j] {
				continue
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestSize {
				bestI, bestJ, bestSize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	return bestI, bestJ, bestSize
}
