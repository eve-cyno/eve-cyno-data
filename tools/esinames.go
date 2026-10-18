package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"eve-cyno.dev/go/data/esi"
)

const (
	// esiNamesTTL is how long a name from /universe/names stays cached. Alliance and
	// corporation names change rarely.
	esiNamesTTL = 24 * time.Hour
	// esiNamesMax bounds the cache; past it the oldest entries are dropped.
	esiNamesMax = 20000
	// esiNamesBatch is ESI's limit of ids per /universe/names request.
	esiNamesBatch = 1000
)

type cachedName struct {
	name string
	at   time.Time
	seq  uint64
}

// idNameCache keeps ESI id-to-name lookups. The zero value is ready.
type idNameCache struct {
	mu      sync.Mutex
	entries map[int]cachedName
	seq     uint64
	now     func() time.Time // nil means time.Now (tests inject a clock)
}

func (n *idNameCache) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

// lookup splits ids into cached names and the ids still to fetch.
func (n *idNameCache) lookup(ids []int) (found map[int]string, missing []int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	found = map[int]string{}
	now := n.clock()
	for _, id := range ids {
		if e, ok := n.entries[id]; ok && now.Sub(e.at) < esiNamesTTL {
			found[id] = e.name
			continue
		}
		missing = append(missing, id)
	}
	return found, missing
}

// store adds names and drops the oldest entries beyond esiNamesMax.
func (n *idNameCache) store(names map[int]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.entries == nil {
		n.entries = map[int]cachedName{}
	}
	now := n.clock()
	for id, name := range names {
		n.seq++
		n.entries[id] = cachedName{name: name, at: now, seq: n.seq}
	}
	if len(n.entries) <= esiNamesMax {
		return
	}
	type aged struct {
		id  int
		seq uint64
	}
	all := make([]aged, 0, len(n.entries))
	for id, e := range n.entries {
		all = append(all, aged{id, e.seq})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
	for _, a := range all[:len(all)-esiNamesMax] {
		delete(n.entries, a.id)
	}
}

// universeNames returns names for ids, POSTing /universe/names (at most 1000 ids a
// request) only for ids not cached; it caches what it gets. An error leaves the part
// found so far (cached or fetched) in the map.
func (c *Client) universeNames(ctx context.Context, ids []int) (map[int]string, error) {
	seen := map[int]bool{}
	var unique []int
	for _, id := range ids {
		if id != 0 && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return map[int]string{}, nil
	}
	out, missing := c.names.lookup(unique)
	for len(missing) > 0 {
		chunk := missing
		if len(chunk) > esiNamesBatch {
			chunk = chunk[:esiNamesBatch]
		}
		missing = missing[len(chunk):]
		payload, err := json.Marshal(chunk)
		if err != nil {
			return out, fmt.Errorf("encoding name ids: %w", err)
		}
		resp, err := c.esiClient().Do(ctx, esi.Request{Method: http.MethodPost, Path: "/universe/names", Body: payload})
		if err != nil {
			return out, fmt.Errorf("fetching names: %w", err)
		}
		if resp.Status != http.StatusOK {
			return out, fmt.Errorf("fetching names: HTTP %d", resp.Status)
		}
		var entries []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(resp.Body, &entries); err != nil {
			return out, fmt.Errorf("parsing names: %w", err)
		}
		got := make(map[int]string, len(entries))
		for _, e := range entries {
			got[e.ID] = e.Name
			out[e.ID] = e.Name
		}
		c.names.store(got)
	}
	return out, nil
}
