package tools

import (
	"strconv"
	"strings"

	"eve-cyno.dev/go/data/fit"
	"eve-cyno.dev/go/data/sde"
)

// entityCategoryID is the SDE category of NPCs ("Entity"): no market volume or Alpha use.
const entityCategoryID = 11

// fillItemFacts completes the rows of items (Query and TypeID set) from the SDE's type facts
// in one query. legality, when not nil, adds the Alpha usability.
func fillItemFacts(s *sde.SDE, legality *fit.Legality, items []ItemFacts) {
	ids := make([]int, len(items))
	for i, it := range items {
		ids[i] = it.TypeID
	}
	facts := s.TypeFactsFor(ids)
	for i := range items {
		it := &items[i]
		it.MarketGroupPath = []string{}
		f, ok := facts[it.TypeID]
		if !ok {
			it.Name = it.Query
			continue
		}
		it.known = true
		it.Name, it.Published = f.Name, f.Published
		it.GroupID, it.Group, it.CategoryID, it.Category = f.GroupID, f.Group, f.CategoryID, f.Category
		if f.MetaGroup != "" {
			mg := f.MetaGroup
			it.MetaGroup = &mg
		}
		it.MetaLevel, it.TechLevel = f.MetaLevel, f.TechLevel
		if f.MarketGroupPath != nil {
			it.MarketGroupPath = f.MarketGroupPath
		}
		it.BasePrice, it.PortionSize = f.BasePrice, f.PortionSize
		it.VolumeM3, it.PackagedVolumeM3, it.CapacityM3, it.MassKg = f.VolumeM3, f.PackagedVolumeM3, f.CapacityM3, f.MassKg
		if legality != nil && it.CategoryID != entityCategoryID {
			alpha := legality.IsAlphaLegalType(it.TypeID)
			it.AlphaUsable = &alpha
		}
	}
}

// itemFactsLine is the details text of one item: "<query>: type_id=<id>" followed by the
// facts that exist, joined by " · ": group and category, meta group and level, market
// group path, volume, Alpha usability.
func itemFactsLine(it ItemFacts) string {
	if !it.known {
		return it.Query + ": type_id=" + strconv.Itoa(it.TypeID)
	}
	parts := []string{}
	if it.Group != "" {
		g := it.Group
		if it.Category != "" {
			g += " (" + it.Category + ")"
		}
		parts = append(parts, g)
	}
	if it.MetaGroup != nil {
		m := *it.MetaGroup
		if it.MetaLevel != nil {
			m += ", meta level " + strconv.Itoa(*it.MetaLevel)
		}
		parts = append(parts, m)
	}
	if len(it.MarketGroupPath) > 0 {
		parts = append(parts, strings.Join(it.MarketGroupPath, " > "))
	}
	if it.CategoryID != entityCategoryID {
		v := fmtM3(it.VolumeM3) + " m³"
		if it.PackagedVolumeM3 != nil && *it.PackagedVolumeM3 != it.VolumeM3 {
			v += " (packaged " + fmtM3(*it.PackagedVolumeM3) + " m³)"
		}
		parts = append(parts, v)
		if it.AlphaUsable != nil {
			if *it.AlphaUsable {
				parts = append(parts, "Alpha: yes")
			} else {
				parts = append(parts, "Alpha: no")
			}
		}
	}
	line := it.Query + ": type_id=" + strconv.Itoa(it.TypeID)
	if len(parts) > 0 {
		line += " · " + strings.Join(parts, " · ")
	}
	return line
}

// fmtM3 formats a volume with thousands separators and at most 2 decimals, trailing zeros trimmed.
func fmtM3(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	frac = strings.TrimRight(frac, "0")
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if frac != "" {
		b.WriteString("." + frac)
	}
	return b.String()
}
