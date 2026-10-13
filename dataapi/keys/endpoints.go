package keys

import "unicode/utf8"

const (
	maxEndpointBytes = 96
	maxEndpoints     = 64 // distinct endpoints per key per day; further ones are "other"
	otherEndpoint    = "other"
)

// cutLabel shortens an endpoint label to maxEndpointBytes without splitting a rune.
func cutLabel(ep string) string {
	if ep == "" {
		return "-"
	}
	if len(ep) <= maxEndpointBytes {
		return ep
	}
	ep = ep[:maxEndpointBytes]
	for len(ep) > 0 && !utf8.ValidString(ep) {
		ep = ep[:len(ep)-1]
	}
	return ep
}

// Record implements dataapi.Usage: it counts one request of key id on endpoint in today's
// per-endpoint and per-day totals, as served or (refused, a 429) refused. It does not touch
// the quota charge, which only Spend moves. An id the store does not hold
// is ignored. The counts are written by the same flusher as the quota counts.
func (s *Store) Record(id, endpoint string, refused bool) {
	s.mu.RLock()
	_, known := s.byID[id]
	s.mu.RUnlock()
	if !known {
		return
	}
	endpoint = cutLabel(endpoint)
	s.lockCurrent()
	defer s.usageMu.Unlock()
	if !s.stillKnown(id) {
		return
	}

	endpoint = s.endpointSlot(id, endpoint)
	m := s.eps[id]
	e := m[endpoint]
	r := s.rows[id]
	if refused {
		e.refused++
		r.refused++
	} else {
		e.count++
		r.served++
	}
	m[endpoint] = e
	s.rows[id] = r
	s.dirty[id] = true
	if s.dirtyEp[id] == nil {
		s.dirtyEp[id] = map[string]bool{}
	}
	s.dirtyEp[id][endpoint] = true
}

// endpointSlot returns the label under which endpoint is counted for key id: itself, or
// "other" once the key has maxEndpoints distinct labels today. The caller holds usageMu.
func (s *Store) endpointSlot(id, endpoint string) string {
	m := s.eps[id]
	if m == nil {
		m = map[string]usageRow{}
		s.eps[id] = m
	}
	if _, seen := m[endpoint]; !seen {
		real := len(m)
		if _, has := m[otherEndpoint]; has {
			real--
		}
		if real >= maxEndpoints {
			return otherEndpoint
		}
	}
	return endpoint
}

// RecordTool counts one tool call inside an MCP request under the endpoint "mcp:<tool>". The
// label is a sub-breakdown of the MCP route, not a request: it never moves the day's served,
// refused or charged counters, so the endpoint rows that do not start with "mcp:" still add
// up to the day totals. A call the upstream limiter then refuses is counted here as well. An
// id the store does not hold is ignored. The counts are written by the usage flusher.
func (s *Store) RecordTool(id, tool string) {
	s.mu.RLock()
	_, known := s.byID[id]
	s.mu.RUnlock()
	if !known {
		return
	}
	endpoint := cutLabel("mcp:" + tool)
	s.lockCurrent()
	defer s.usageMu.Unlock()
	if !s.stillKnown(id) {
		return
	}
	endpoint = s.endpointSlot(id, endpoint)
	m := s.eps[id]
	e := m[endpoint]
	e.count++
	m[endpoint] = e
	if s.dirtyEp[id] == nil {
		s.dirtyEp[id] = map[string]bool{}
	}
	s.dirtyEp[id][endpoint] = true
}
