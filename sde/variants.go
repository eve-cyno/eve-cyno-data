package sde

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// MetaVariants returns the published members of a module's meta family, the type
// itself excluded: the Tech I parent (invMetaTypes.parentTypeID, a T1 base item
// has no parent) plus every sibling that names the same parent. Tech II, the
// named Tech I meta variants (Compact, Enduring, Scoped, ...), storyline,
// faction, deadspace and officer items all hang off one parent, so the family is
// "the same module at every meta tier and the same size". Callers filter by meta
// tier (GetItemMetaTier) and by what they need (CPU / PG, price).
//
// Returns nil when the SDE has no invMetaTypes table, the type has no family, or
// typeID is unknown. IDs are ascending.
func (s *SDE) MetaVariants(typeID int) []int {
	if s == nil || s.db == nil || typeID <= 0 || !s.HasTable("invMetaTypes") {
		return nil
	}
	// invMetaTypes stores its IDs as TEXT: bind the typeID as text so the
	// (typeID) index is used instead of a CAST scan.
	base := strconv.Itoa(typeID)
	var parent *string
	if err := s.db.QueryRow(`SELECT parentTypeID FROM invMetaTypes WHERE typeID = ? LIMIT 1`, base).Scan(&parent); err == nil &&
		parent != nil && *parent != "" {
		base = *parent
	}
	rows, err := s.db.Query(`
		SELECT t.typeID
		FROM invTypes t
		WHERE t.published = 1
		  AND (t.typeID = CAST(? AS INTEGER)
		       OR t.typeID IN (SELECT CAST(typeID AS INTEGER) FROM invMetaTypes WHERE parentTypeID = ?))
		ORDER BY t.typeID`, base, base)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil || id == typeID {
			continue
		}
		out = append(out, id)
	}
	return out
}

// nameCategories are the SDE categories a fabricated fit-line name can stand for:
// 7 Module (rigs are modules too), 18 Drone, 32 Subsystem.
const nameCategories = "7,18,32"

// nameTier splits a trailing tier token (I..V) from a module name's words.
func nameTier(words []string) (content []string, tier string) {
	if n := len(words); n > 0 && romanTier(strings.ToUpper(words[n-1])) {
		return words[:n-1], words[n-1]
	}
	return words, ""
}

// nameWords lower-cases a name and splits it into words on anything that is not a
// letter or digit; "Nano-Membrane" and "Nano Membrane" tokenise alike.
func nameWords(name string) []string {
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// stemWord drops a plural "s" so "Hybrids" matches "Hybrid"; it leaves short
// words and "-ss" words alone.
func stemWord(w string) string {
	if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
		return w[:len(w)-1]
	}
	return w
}

// weightedWords maps each stemmed content word to its weight: 2 for the last word
// (the module's own noun: "Membrane", "Launcher", "Repairer" - the one thing a
// rename never changes), 1 for the others (size, race, variant words).
func weightedWords(content []string) map[string]float64 {
	out := make(map[string]float64, len(content))
	for i, w := range content {
		w = stemWord(w)
		weight := 1.0
		if i == len(content)-1 {
			weight = 2.0
		}
		if cur, ok := out[w]; !ok || weight > cur {
			out[w] = weight
		}
	}
	return out
}

// wordSynonyms maps a word of a stale or abbreviated module name to the word the
// SDE uses now, so the pair counts as a match in NearestModuleNames. Only renames
// and spellings seen in model output and in the SDE belong here:
//
//   - adaptive: CCP renamed the omni-resist armor membrane "Energized Adaptive Nano
//     Membrane" to "Multispectrum Energized Membrane" (the six resist variants of the
//     group tie on every other word, so this is what puts Multispectrum first);
//   - armour: British spelling, the SDE says "armor";
//   - thermic: the pre-rename "Thermic" of "Thermal";
//   - cap, invuln: abbreviations models write ("Cap Booster", "Adaptive Invuln Field").
var wordSynonyms = map[string]string{
	"adaptive": "multispectrum",
	"armour":   "armor",
	"thermic":  "thermal",
	"cap":      "capacitor",
	"invuln":   "invulnerability",
}

// Score weights of NearestModuleNames: the word overlap decides; a matching tier
// suffix, a shared group with an old (unpublished) item of the same words, and
// the character-level ratio only order what the overlap leaves tied.
const (
	nearestTierBonus     = 0.10
	nearestGroupBonus    = 0.30
	nearestRatioWeight   = 0.15
	nearestMinOverlap    = 0.30
	nearestMaxCandidates = 4000
)

// NearestModuleNames returns up to limit published module / rig / drone /
// subsystem names closest to a name that does not resolve in the SDE: a
// fabricated name or one CCP has renamed ("Energized Adaptive Nano Membrane II" is
// now "Multispectrum Energized Membrane II"). It is deterministic and uses no LLM:
//
//   - candidates are published items of categories Module, Drone and Subsystem
//     that share at least one word with the query;
//   - the score is the weighted word overlap (Dice) of the names without their tier
//     suffix, the last word - the module's noun - counting double, plus a bonus for
//     the same tier suffix (" II" for " II"), a bonus when the candidate sits in a
//     group that also holds an unpublished item with all the query's words (the old
//     name of a renamed module: "Scan Probe Launcher II" lives on, unpublished, in
//     the group of Core / Expanded Probe Launcher II) and a small character-ratio
//     term that orders the rest;
//   - ties go to the shorter, then the alphabetically first name.
//
// Returns nil for an empty or unrelated name.
func (s *SDE) NearestModuleNames(name string, limit int) []string {
	if s == nil || s.db == nil {
		return nil
	}
	if limit <= 0 {
		limit = 3
	}
	qContent, qTier := nameTier(nameWords(name))
	if len(qContent) == 0 {
		return nil
	}
	qWords := weightedWords(qContent)
	queryNorm := strings.Join(nameWords(name), " ")

	// Words used to pull candidates: at least 3 characters, so "mm" and "x" do not
	// match half the SDE.
	var likes []string
	var args []any
	for w := range qWords {
		if len(w) < 3 {
			continue
		}
		likes = append(likes, "t.typeName LIKE ?")
		args = append(args, "%"+w+"%")
	}
	if len(likes) == 0 {
		return nil
	}

	affinity := s.oldNameGroups(qWords)

	rows, err := s.db.Query(`
		SELECT t.typeName, t.groupID
		FROM invTypes t
		JOIN invGroups g ON g.groupID = t.groupID
		WHERE t.published = 1 AND g.categoryID IN (`+nameCategories+`)
		  AND (`+strings.Join(likes, " OR ")+`)
		LIMIT `+strconv.Itoa(nearestMaxCandidates), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type scored struct {
		name  string
		score float64
	}
	var cands []scored
	seen := map[string]bool{}
	for rows.Next() {
		var cName string
		var groupID int
		if err := rows.Scan(&cName, &groupID); err != nil || seen[cName] || strings.HasPrefix(cName, "Mutated ") {
			continue // a mutaplasmid-rolled module is never what a fit line means
		}
		seen[cName] = true
		cWords, cTier := nameTier(nameWords(cName))
		if len(cWords) == 0 {
			continue
		}
		cw := weightedWords(cWords)
		var inter, qTotal, cTotal float64
		for w, wt := range qWords {
			qTotal += wt
			if cwt, ok := cw[w]; ok {
				inter += min(wt, cwt)
			} else if cwt, ok := cw[wordSynonyms[w]]; ok && wordSynonyms[w] != "" {
				inter += min(wt, cwt)
			}
		}
		for _, wt := range cw {
			cTotal += wt
		}
		if inter == 0 {
			continue
		}
		dice := 2 * inter / (qTotal + cTotal)
		if dice < nearestMinOverlap {
			continue
		}
		score := dice + nearestRatioWeight*sequencerRatio(queryNorm, strings.Join(nameWords(cName), " "))
		if qTier != "" && qTier == cTier {
			score += nearestTierBonus
		}
		if affinity[groupID] {
			score += nearestGroupBonus
		}
		cands = append(cands, scored{cName, score})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if len(cands[i].name) != len(cands[j].name) {
			return len(cands[i].name) < len(cands[j].name)
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.name
	}
	return out
}

// oldNameGroups returns the groups of the unpublished module-like items whose
// names contain every word of the query: CCP keeps the old type of a renamed
// module in the SDE, unpublished, in the group the new one lives in.
func (s *SDE) oldNameGroups(qWords map[string]float64) map[int]bool {
	out := map[int]bool{}
	var conds []string
	var args []any
	for w := range qWords {
		conds = append(conds, "t.typeName LIKE ?")
		args = append(args, "%"+w+"%")
	}
	if len(conds) == 0 {
		return out
	}
	rows, err := s.db.Query(`
		SELECT DISTINCT t.groupID
		FROM invTypes t
		JOIN invGroups g ON g.groupID = t.groupID
		WHERE t.published = 0 AND g.categoryID IN (`+nameCategories+`)
		  AND `+strings.Join(conds, " AND ")+`
		LIMIT 20`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var gid int
		if err := rows.Scan(&gid); err == nil {
			out[gid] = true
		}
	}
	return out
}
