package sde

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
)

// RequiredColumns is the single source of truth for the SDE columns the readers in
// this package depend on, per table. The readers swallow query errors (a missing
// column makes GetEffectModifiers return nil, GetAttributeMeta a zero value,
// GetDogma drop mass/volume/capacity), so a schema that lacks one of these degrades
// silently: no skill/ship modifiers reach the dogma engine, no stacking penalties.
//
// Two checks consume it: the SDE builder's publish gate in core/sde/build (a build
// missing a column never replaces the live file) and the open-time check in openHandle
// (an already-deployed file missing one is reported at ERROR).
//
// Keep it in step with the CREATE TABLE statements in
// core/sde/build/builder.go and with the queries in effects.go / dogma.go.
var RequiredColumns = map[string][]string{
	"invTypes":          {"typeID", "groupID", "typeName", "published", "mass", "volume", "capacity"},
	"dgmEffects":        {"effectID", "effectName", "effectCategory", "modifierInfo"},
	"dgmAttributeTypes": {"attributeID", "attributeName", "defaultValue", "stackable", "highIsGood"},
	// The Alpha clone skill caps (AlphaCloneSkills). They are loaded by the generic
	// phase of the build, whose failures are non-fatal, so the gate must insist on them.
	"chrCloneGrades":      {"cloneGradeID", "name"},
	"chrCloneGradeSkills": {"cloneGradeID", "typeID", "level"},
}

// MissingRequiredColumns returns every "table.column" from RequiredColumns that db
// lacks, sorted. A table that does not exist reports all of its columns.
func MissingRequiredColumns(db *sql.DB) ([]string, error) {
	var missing []string
	for table, cols := range RequiredColumns {
		have, err := tableColumns(db, table)
		if err != nil {
			return nil, err
		}
		for _, c := range cols {
			if !have[c] {
				missing = append(missing, table+"."+c)
			}
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// tableColumns returns the set of column names of table (empty when it does not exist).
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	// table_info is a table-valued pragma function: the name is bound, not interpolated.
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("sde: table_info %q: %w", table, err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sde: table_info %q: %w", table, err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sde: table_info %q: %w", table, err)
	}
	return have, nil
}

// logMissingRequiredColumns reports, at ERROR, a freshly opened SDE file that lacks
// RequiredColumns. It never fails the open: the file still serves everything else.
func logMissingRequiredColumns(db *sql.DB, path string) {
	missing, err := MissingRequiredColumns(db)
	if err != nil {
		slog.Warn("sde: required-column check failed", "path", path, "error", err)
		return
	}
	if len(missing) == 0 {
		return
	}
	slog.Error("sde: file lacks columns the readers need; dogma modifiers, stacking, mass/volume/capacity degrade silently - rebuild it (go -C ingest run ./cmd/ingest sde)",
		"path", path, "missing", missing)
}
