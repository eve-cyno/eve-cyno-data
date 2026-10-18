// Package keys is the SQLite-backed store of self-serve API keys for the data API: it
// creates and revokes keys, authenticates requests by the SHA-256 hash of the presented
// key (the secret itself is never stored), counts requests per key and UTC day against a
// daily cap, keeps a 30-day per-endpoint request history, lets an operator change caps
// and block owners, and keeps revoked keys for 30 days with when they were revoked and by
// which role (owner, admin or operator).
//
// A Store implements dataapi.Authenticator, dataapi.Quota and dataapi.Usage. Active keys
// are indexed in memory, so a revoked key (or a blocked owner's keys) stops working at
// once in the same process. Usage counts are kept in memory and written to the database
// periodically, on day rollover and on Close.
//
// The store also keeps accounts. Each sign-in identity (an EVE character or a Discord
// user) belongs to one account, so the keys and limits of every linked identity are
// shared. For an identity the store keeps only the provider, the id, the EVE owner hash
// and the link date. For an account it keeps the creation time and, for testers, the
// time tester evidence was last seen. An account can be deleted with everything the
// store holds for it: by its owner (DeleteAccountOf), unless it is blocked, or by the
// operator (DeleteAccount), which keeps a block on its sign-in ids.
//
// The database file belongs to one Store in one process at a time: the in-memory counts
// are the source of truth for today and overwrite the stored rows. On shutdown stop the
// HTTP server first (so no request is still being counted), then Close the Store.
package keys
