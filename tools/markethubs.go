package tools

import "strings"

// marketHub is one of the five main trade hubs: the region whose order book holds it and
// the station whose orders the hub filter keeps.
type marketHub struct {
	Key       string // lower-case name the tool takes: "jita"
	RegionID  int
	StationID int
	Station   string // the station's SDE name
}

// marketHubs lists the trade hubs get_market_price knows, in the order the tool offers
// them. TestHubStationsExistInTheSDE keeps the ids and names equal to the SDE's.
func marketHubs() []marketHub {
	return []marketHub{
		{"jita", 10000002, 60003760, "Jita 4 - Moon 4 - Caldari Navy Assembly Plant"},
		{"amarr", 10000043, 60008494, "Amarr VIII (Oris) - Emperor Family Academy"},
		{"dodixie", 10000032, 60011866, "Dodixie 9 - Moon 20 - Federation Navy Assembly Plant"},
		{"rens", 10000030, 60004588, "Rens 6 - Moon 8 - Brutor Tribe Treasury"},
		{"hek", 10000042, 60005686, "Hek 8 - Moon 12 - Boundless Creation Factory"},
	}
}

// hubByKey finds a hub by its key, ignoring case and surrounding spaces.
func hubByKey(key string) (marketHub, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, h := range marketHubs() {
		if h.Key == key {
			return h, true
		}
	}
	return marketHub{}, false
}
