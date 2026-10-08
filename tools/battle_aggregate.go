package tools

// battle_aggregate.go — pure helper functions for analyze_battle.
// Mirrors Python _battle_flag_to_slot / _aggregate_damage_by_weapon /
// _aggregate_destroyed_modules / _format_battle_breakdown /
// _resolve_system_name_from_killmails from the original Python implementation.
// No network calls — all inputs come from caller-supplied JSON-decoded data.

import (
	"fmt"
	"sort"
	"strings"

	"eve-cyno.dev/go/data/sde"
)

// ── ESI killmail shapes ────────────────────────────────────────────────────

// KillmailVictimItem mirrors ESI victim.items[i].
type KillmailVictimItem struct {
	Flag              int `json:"flag"`
	ItemTypeID        int `json:"item_type_id"`
	QuantityDropped   int `json:"quantity_dropped"`
	QuantityDestroyed int `json:"quantity_destroyed"`
}

// KillmailVictim mirrors the ESI victim sub-object.
type KillmailVictim struct {
	ShipTypeID    int                  `json:"ship_type_id"`
	AllianceID    int                  `json:"alliance_id"`
	CorporationID int                  `json:"corporation_id"`
	CharacterID   int                  `json:"character_id"`
	FactionID     int                  `json:"faction_id"`
	Items         []KillmailVictimItem `json:"items"`
}

// KillmailAttacker mirrors ESI attackers[i].
type KillmailAttacker struct {
	WeaponTypeID  int  `json:"weapon_type_id"`
	DamageDone    int  `json:"damage_done"`
	ShipTypeID    int  `json:"ship_type_id"`
	AllianceID    int  `json:"alliance_id"`
	CorporationID int  `json:"corporation_id"`
	CharacterID   int  `json:"character_id"`
	FactionID     int  `json:"faction_id"`
	FinalBlow     bool `json:"final_blow"`
}

// Killmail is a single ESI-shaped killmail used by the aggregators.
type Killmail struct {
	KillmailID    int                `json:"killmail_id"`
	SolarSystemID int                `json:"solar_system_id"`
	Victim        KillmailVictim     `json:"victim"`
	Attackers     []KillmailAttacker `json:"attackers"`
}

// ── _BATTLE_SLOT_RANGES ────────────────────────────────────────────────────

type battleSlotRange struct {
	name string
	lo   int
	hi   int
}

// battleSlotRanges mirrors Python _BATTLE_SLOT_RANGES verbatim.
// Order preserved: High → Mid → Low → Rigs → Subsystems.
var battleSlotRanges = []battleSlotRange{
	{"High slots", 27, 34},
	{"Mid slots", 19, 26},
	{"Low slots", 11, 18},
	{"Rigs", 92, 94},
	{"Subsystems", 125, 128},
}

// battleFlagToSlot mirrors Python _battle_flag_to_slot(flag).
// Returns the slot-class name ("High slots", "Mid slots", etc.) or "" if
// the flag falls outside all fitted-slot ranges (cargo, drone bay, etc.).
func battleFlagToSlot(flag int) string {
	for _, r := range battleSlotRanges {
		if flag >= r.lo && flag <= r.hi {
			return r.name
		}
	}
	return ""
}

// ── aggregateDamageByWeapon ────────────────────────────────────────────────

// WeaponEntry holds the aggregated stats for one weapon type on one side.
type WeaponEntry struct {
	WeaponTypeID int
	TotalDamage  int
	KillCount    int
}

// aggregateDamageByWeapon mirrors Python _aggregate_damage_by_weapon.
// Returns {side → top-10 weapons sorted by damage desc}.
// sideLookup maps killmail_id → side label ("Side A", "Side 1", etc.).
// Missing entries bucket as "Unknown".
func aggregateDamageByWeapon(killmails []Killmail, sideLookup map[int]string) map[string][]WeaponEntry {
	// side → weaponTypeID → [totalDamage, killCount]
	type row struct{ dmg, kc int }
	bucket := map[string]map[int]*row{}

	for i := range killmails {
		km := &killmails[i]
		side, ok := sideLookup[km.KillmailID]
		if !ok {
			side = "Unknown"
		}
		sideMap := bucket[side]
		if sideMap == nil {
			sideMap = map[int]*row{}
			bucket[side] = sideMap
		}
		seenWeapons := map[int]bool{}
		for j := range km.Attackers {
			atk := &km.Attackers[j]
			wid := atk.WeaponTypeID
			dmg := atk.DamageDone
			if wid == 0 || dmg == 0 {
				continue
			}
			r := sideMap[wid]
			if r == nil {
				r = &row{}
				sideMap[wid] = r
			}
			r.dmg += dmg
			// Count each killmail once per weapon even if multiple attackers
			// used the same weapon on the same victim.
			if !seenWeapons[wid] {
				r.kc++
				seenWeapons[wid] = true
			}
		}
	}

	result := map[string][]WeaponEntry{}
	for side, weapons := range bucket {
		entries := make([]WeaponEntry, 0, len(weapons))
		for wid, r := range weapons {
			entries = append(entries, WeaponEntry{wid, r.dmg, r.kc})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].TotalDamage > entries[j].TotalDamage })
		if len(entries) > 10 {
			entries = entries[:10]
		}
		result[side] = entries
	}
	return result
}

// ── aggregateDestroyedModules ──────────────────────────────────────────────

// ModuleEntry holds count of a destroyed module type in one slot class on one side.
type ModuleEntry struct {
	TypeID int
	Count  int
}

// aggregateDestroyedModules mirrors Python _aggregate_destroyed_modules.
// Returns {side → {slot_class → top-10 modules sorted by count desc}}.
// sideLookup maps killmail_id → side label.
func aggregateDestroyedModules(killmails []Killmail, sideLookup map[int]string) map[string]map[string][]ModuleEntry {
	// side → slot → typeID → count
	bucket := map[string]map[string]map[int]int{}

	for i := range killmails {
		km := &killmails[i]
		side, ok := sideLookup[km.KillmailID]
		if !ok {
			side = "Unknown"
		}
		sideMap := bucket[side]
		if sideMap == nil {
			sideMap = map[string]map[int]int{}
			bucket[side] = sideMap
		}
		for j := range km.Victim.Items {
			item := &km.Victim.Items[j]
			slot := battleFlagToSlot(item.Flag)
			if slot == "" {
				continue
			}
			tid := item.ItemTypeID
			if tid == 0 {
				continue
			}
			qty := item.QuantityDropped + item.QuantityDestroyed
			if qty == 0 {
				qty = 1
			}
			slotMap := sideMap[slot]
			if slotMap == nil {
				slotMap = map[int]int{}
				sideMap[slot] = slotMap
			}
			slotMap[tid] += qty
		}
	}

	result := map[string]map[string][]ModuleEntry{}
	for side, slots := range bucket {
		sideOut := map[string][]ModuleEntry{}
		for slotName, modules := range slots {
			entries := make([]ModuleEntry, 0, len(modules))
			for tid, cnt := range modules {
				entries = append(entries, ModuleEntry{tid, cnt})
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Count > entries[j].Count })
			if len(entries) > 10 {
				entries = entries[:10]
			}
			sideOut[slotName] = entries
		}
		result[side] = sideOut
	}
	return result
}

// ── formatBattleBreakdown ──────────────────────────────────────────────────

// formatBattleBreakdown mirrors Python _format_battle_breakdown exactly.
// Resolves type IDs → names via SDE batch lookup.
// Falls back to "Type#<N>" when the SDE is unavailable or the ID is unknown.
func formatBattleBreakdown(
	s *sde.SDE,
	aggDmg map[string][]WeaponEntry,
	aggMods map[string]map[string][]ModuleEntry,
) string {
	// Collect all type IDs for one batch SDE lookup.
	allIDs := map[int]struct{}{}
	for _, weapons := range aggDmg {
		for _, w := range weapons {
			allIDs[w.WeaponTypeID] = struct{}{}
		}
	}
	for _, slots := range aggMods {
		for _, modules := range slots {
			for _, m := range modules {
				allIDs[m.TypeID] = struct{}{}
			}
		}
	}

	var names map[int]string
	if len(allIDs) > 0 && s != nil && s.Available() {
		idSlice := make([]int, 0, len(allIDs))
		for id := range allIDs {
			idSlice = append(idSlice, id)
		}
		names = s.GetTypeNames(idSlice)
	}

	name := func(tid int) string {
		if n, ok := names[tid]; ok && n != "" {
			return n
		}
		return fmt.Sprintf("Type#%d", tid)
	}

	lines := []string{"", "### Damage by weapon type"}
	if len(aggDmg) == 0 {
		lines = append(lines, "  (no killmails resolved)")
	} else {
		// Sort sides alphabetically to match Python sorted(agg_dmg).
		sides := make([]string, 0, len(aggDmg))
		for side := range aggDmg {
			sides = append(sides, side)
		}
		sort.Strings(sides)

		for _, side := range sides {
			ranked := aggDmg[side]
			if len(ranked) == 0 {
				continue
			}
			lines = append(lines, fmt.Sprintf("**%s** — top weapons by damage dealt:", side))
			for _, w := range ranked {
				lines = append(lines, fmt.Sprintf("  • %s — %s dmg across %d kill(s)",
					name(w.WeaponTypeID), fmtThousands(w.TotalDamage), w.KillCount))
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, "### Modules destroyed by slot")
	if len(aggMods) == 0 {
		lines = append(lines, "  (no killmails resolved)")
	} else {
		sides := make([]string, 0, len(aggMods))
		for side := range aggMods {
			sides = append(sides, side)
		}
		sort.Strings(sides)

		// Stable slot order: Hi → Mid → Low → Rigs → Subsystems — mirrors Python slot_order.
		slotOrder := make([]string, len(battleSlotRanges))
		for i, r := range battleSlotRanges {
			slotOrder[i] = r.name
		}

		for _, side := range sides {
			slots := aggMods[side]
			if len(slots) == 0 {
				continue
			}
			lines = append(lines, fmt.Sprintf("**%s** — most destroyed modules:", side))
			for _, slotName := range slotOrder {
				ranked, ok := slots[slotName]
				if !ok || len(ranked) == 0 {
					continue
				}
				lines = append(lines, fmt.Sprintf("  %s:", slotName))
				for _, m := range ranked {
					lines = append(lines, fmt.Sprintf("    • %s ×%d", name(m.TypeID), m.Count))
				}
			}
		}
	}

	return strings.Join(lines, "\n")
}

// fmtThousands formats an integer with comma thousands separators,
// matching Python {:,} (e.g. 1234567 → "1,234,567").
func fmtThousands(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		s = s[1:]
	}
	// Insert commas every 3 digits from the right.
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if n < 0 {
		return "-" + b.String()
	}
	return b.String()
}

// ── resolveSystemNameFromKillmails ─────────────────────────────────────────

// resolveSystemNameFromKillmails mirrors Python _resolve_system_name_from_killmails.
// Iterates killmails looking for a solar_system_id and resolves it via SDE.
// Returns "" if no name can be resolved (SDE unavailable, no system IDs, etc.).
func resolveSystemNameFromKillmails(s *sde.SDE, killmails []Killmail) string {
	if s == nil || !s.Available() {
		return ""
	}
	for i := range killmails {
		sid := killmails[i].SolarSystemID
		if sid == 0 {
			continue
		}
		if n := s.GetSystemName(sid); n != nil && *n != "" {
			return *n
		}
	}
	return ""
}
