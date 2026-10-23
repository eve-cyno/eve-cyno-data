package tools

import "fmt"

// Per-call limits of the public tools. Each one is mirrored as maxItems / maximum in
// tool_schemas.json; TestSchemaCapsMatchTheCode keeps the two equal. A request over a cap
// is answered with one text line and no typed data; a list is never truncated silently.
const (
	maxDistanceTargets   = 200 // get_jump_distances: systems
	maxAvoid             = 20  // avoid lists
	maxVia               = 5   // get_jumps_between: via
	maxSystemsBatch      = 50  // systems per batch call
	maxPriceItems        = 200 // items per price call
	maxHistoryDays       = 90  // market history window
	maxFindLimit         = 100 // find_systems: limit
	defaultFindLimit     = 20
	maxNearJumps         = 30 // near.max_jumps
	maxTheraLimit        = 50 // Thera connections: limit
	defaultTheraLimit    = 20
	maxFilterValues      = 12            // values in one filter list
	maxWaypoints         = 5             // plan_jump_route: waypoints
	maxAvoidRegions      = 10            // plan_jump_route: avoid_regions
	maxSkillLevel        = 5             // skill levels, 0 to maxSkillLevel
	maxRouteJumps        = 100           // plan_jump_route: jumps listed in one answer
	maxReprocessQuantity = 1_000_000_000 // get_reprocessing_yield: quantity
	maxProductionRuns    = 10_000        // get_production_chain: runs
	// maxItemFacts is conditional (names with details), so the schema has no maxItems for it:
	// the tool text and a test pin it instead.
	maxItemFacts = 50 // search_item_by_name: names with details
)

// listCapError is the answer for a list longer than its cap.
func listCapError(param string, max, got int) string {
	return fmt.Sprintf("%s: at most %d per call (got %d); split the list.", param, max, got)
}

// rangeError is the answer for a number outside its range.
func rangeError(param string, lo, hi, got int) string {
	return fmt.Sprintf("%s: %d to %d (got %d).", param, lo, hi, got)
}
