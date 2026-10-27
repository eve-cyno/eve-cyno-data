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
