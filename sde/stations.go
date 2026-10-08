package sde

import (
	"fmt"
	"strings"
)

// GetNPCStations returns NPC station rows filtered by optional systemID, corpID, operationID.
func (s *SDE) GetNPCStations(systemID, corpID, operationID *int) []map[string]any {
	if !s.HasTable("staStations") {
		return nil
	}
	var clauses []string
	var params []any
	if systemID != nil {
		clauses = append(clauses, "solarSystemID = ?")
		params = append(params, *systemID)
	}
	if corpID != nil {
		clauses = append(clauses, "corporationID = ?")
		params = append(params, *corpID)
	}
	if operationID != nil {
		clauses = append(clauses, "operationID = ?")
		params = append(params, *operationID)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	params = append(params, 500)
	rows, err := s.db.Query(fmt.Sprintf(
		"SELECT stationID, solarSystemID, corporationID, stationName, operationID "+
			"FROM staStations %s ORDER BY stationName LIMIT ?", where), params...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []map[string]any
	for rows.Next() {
		var stID, sysID, corpIDVal, opID int
		var stName string
		if err := rows.Scan(&stID, &sysID, &corpIDVal, &stName, &opID); err != nil {
			continue
		}
		result = append(result, map[string]any{
			"station_id":     stID,
			"system_id":      sysID,
			"corporation_id": corpIDVal,
			"station_name":   stName,
			"operation_id":   opID,
		})
	}
	return result
}

// GetStationServices returns [serviceID, ...] for the given station operationID.
func (s *SDE) GetStationServices(operationID int) []int {
	table := s.servicesJoinTable()
	if table == "" {
		return nil
	}
	rows, err := s.db.Query(
		fmt.Sprintf("SELECT serviceID FROM %s WHERE operationID = ?", table), operationID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err == nil {
			result = append(result, id)
		}
	}
	return result
}

// GetServiceNames returns {serviceID: serviceName} for a batch of service IDs.
func (s *SDE) GetServiceNames(serviceIDs []int) map[int]string {
	if len(serviceIDs) == 0 || !s.HasTable("staServices") {
		return map[int]string{}
	}
	// Check for serviceName column
	cols, err := s.tableColumns("staServices")
	if err != nil || !sliceContains(cols, "serviceName") {
		return map[int]string{}
	}
	placeholders := strings.Repeat("?,", len(serviceIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(serviceIDs))
	for i, id := range serviceIDs {
		args[i] = id
	}
	rows, err := s.db.Query(
		"SELECT serviceID, serviceName FROM staServices WHERE serviceID IN ("+placeholders+")", args...)
	if err != nil {
		return map[int]string{}
	}
	defer rows.Close()
	result := make(map[int]string)
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			result[id] = name
		}
	}
	return result
}

// FindAgents searches agtAgents with optional filters (all AND-joined).
func (s *SDE) FindAgents(corporationID, level, divisionID *int, agentType *string, regionID *int, inSpaceOnly bool, limit int) []map[string]any {
	if !s.HasTable("agtAgents") {
		return nil
	}
	if limit <= 0 {
		limit = 25
	}
	var clauses []string
	var params []any
	if corporationID != nil {
		clauses = append(clauses, "a.corporationID = ?")
		params = append(params, fmt.Sprintf("%d", *corporationID))
	}
	if level != nil {
		clauses = append(clauses, "a.level = ?")
		params = append(params, fmt.Sprintf("%d", *level))
	}
	if divisionID != nil {
		clauses = append(clauses, "a.divisionID = ?")
		params = append(params, fmt.Sprintf("%d", *divisionID))
	}
	if agentType != nil {
		clauses = append(clauses, "at.agentType = ?")
		params = append(params, *agentType)
	}
	if regionID != nil {
		clauses = append(clauses, "s.regionID = ?")
		params = append(params, *regionID)
	}
	if inSpaceOnly && s.HasTable("agtAgentsInSpace") {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM agtAgentsInSpace sp WHERE sp.agentID = a.agentID)")
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	params = append(params, limit)
	sql := fmt.Sprintf(`
		SELECT
			a.agentID, a.level, a.quality, a.corporationID, a.divisionID, a.isLocator,
			at.agentType,
			n.itemName        AS agentName,
			CAST(a.locationID AS INTEGER) AS stationID,
			st.stationName,
			st.solarSystemID  AS systemID,
			s.solarSystemName AS systemName,
			s.security        AS systemSecurity,
			s.regionID        AS regionID,
			r.regionName      AS regionName
		FROM agtAgents a
		LEFT JOIN agtAgentTypes at ON at.agentTypeID = a.agentTypeID
		LEFT JOIN invNames n       ON n.itemID = a.agentID
		LEFT JOIN staStations st   ON st.stationID = CAST(a.locationID AS INTEGER)
		LEFT JOIN mapSolarSystems s ON s.solarSystemID = st.solarSystemID
		LEFT JOIN mapRegions r     ON r.regionID = s.regionID
		%s
		ORDER BY CAST(a.agentID AS INTEGER)
		LIMIT ?`, where)

	rows, err := s.db.Query(sql, params...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var agentID *string
		var lvl, quality, corpID, divID, isLocator *string
		var agentTypeStr, agentName, stationName, systemName, regionName *string
		var stationID, systemID *int
		var systemSec *float64
		var regionIDVal *int
		if err := rows.Scan(&agentID, &lvl, &quality, &corpID, &divID, &isLocator,
			&agentTypeStr, &agentName, &stationID, &stationName, &systemID,
			&systemName, &systemSec, &regionIDVal, &regionName); err != nil {
			continue
		}
		isLoc := false
		if isLocator != nil {
			if n := safeIntText(isLocator); n != nil && *n != 0 {
				isLoc = true
			}
		}
		out = append(out, map[string]any{
			"agent_id":       safeIntText(agentID),
			"agent_name":     derefStr(agentName),
			"level":          safeIntText(lvl),
			"quality":        safeIntText(quality),
			"agent_type":     derefStr(agentTypeStr),
			"corporation_id": safeIntText(corpID),
			"division_id":    safeIntText(divID),
			"is_locator":     isLoc,
			"station_id":     derefIntPtr(stationID),
			"station_name":   derefStr(stationName),
			"system_id":      derefIntPtr(systemID),
			"system_name":    derefStr(systemName),
			"region_id":      derefIntPtr(regionIDVal),
			"region_name":    derefStr(regionName),
			"security":       derefFloat(systemSec),
		})
	}
	return out
}

// servicesJoinTable returns the operationID→serviceID join table name.
func (s *SDE) servicesJoinTable() string {
	if s.HasTable("staOperationServices") {
		return "staOperationServices"
	}
	if s.HasTable("staServices") {
		cols, err := s.tableColumns("staServices")
		if err == nil && sliceContains(cols, "operationID") {
			return "staServices"
		}
	}
	return ""
}

func (s *SDE) tableColumns(table string) ([]string, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err == nil {
			cols = append(cols, name)
		}
	}
	return cols, nil
}

func sliceContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
