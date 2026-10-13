// Package keys is the SQLite-backed store of self-serve API keys for the data API: it
// creates and revokes keys, authenticates requests by the SHA-256 hash of the presented
// key (the secret itself is never stored), counts requests per key and UTC day against a
// daily cap, keeps a 30-day per-endpoint request history and lets an operator change caps
// and block owners.
//
// A Store implements dataapi.Authenticator, dataapi.Quota and dataapi.Usage. Active keys
// are indexed in memory, so a revoked key (or a blocked owner's keys) stops working at
// once in the same process. Usage counts are kept in memory and written to the database
// periodically, on day rollover and on Close.
//
// The database file belongs to one Store in one process at a time: the in-memory counts
// are the source of truth for today and overwrite the stored rows. On shutdown stop the
// HTTP server first (so no request is still being counted), then Close the Store.
package keys
