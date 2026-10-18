package keys

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"modernc.org/sqlite" // pure-Go SQLite driver
	sqlitelib "modernc.org/sqlite/lib"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/dataapi"
)

const (
	secretPrefix   = "ecd_"
	defaultPerUser = 3
	defaultFlush   = 30 * time.Second
	maxNameRunes   = 40
	maxOwnerRunes  = 64

	// MaxCreatesPerDay is how many keys one owner may create per UTC day, revoked ones
	// included. Keys an operator issues still count toward it when the owner creates one
	// later the same day; only the operator's own Issue call is exempt from the check.
	MaxCreatesPerDay = 10
	// revokedRetainDays is how long a revoked key's row (and its usage) is kept.
	revokedRetainDays = 30
)

var (
	ErrTooManyKeys = errors.New("too many active keys")
	ErrNotFound    = errors.New("no such key")
	ErrBadOwner    = errors.New("invalid owner id")
	ErrBadName     = errors.New("invalid key name")
	// ErrCreateLimit is returned by Create when the owner already created MaxCreatesPerDay
	// keys today (UTC).
	ErrCreateLimit = errors.New("too many keys created today")
	// ErrOwnerBlocked is returned by Create for an owner an operator has blocked.
	ErrOwnerBlocked = errors.New("owner is blocked")
	// ErrBadCap is returned by SetCap for a cap outside 0..10_000_000.
	ErrBadCap = errors.New("invalid daily cap")
	// ErrBadDays is returned by Usage for a window outside 1..30 days.
	ErrBadDays = errors.New("invalid usage window")
)

var (
	_ dataapi.Authenticator = (*Store)(nil)
	_ dataapi.Quota         = (*Store)(nil)
	_ dataapi.Usage         = (*Store)(nil)
)

// OwnerPattern is the accepted shape of a key owner id: a canonical account ("acct:" and 16 lowercase hex digits)
// or a sign-in identity ("eve:<character id>", "discord:<user id>"), the form keys were owned by before accounts
// existed. A sign-in id that is linked to an account acts for that account. Callers that validate an owner before
// it reaches the store (for example an admin proxy) use this constant so the rule cannot drift.
const OwnerPattern = `^(?:acct:[0-9a-f]{16}|(?:eve|discord):[A-Za-z0-9._-]{1,128})$`

var ownerRE = regexp.MustCompile(OwnerPattern)

const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
  id          TEXT PRIMARY KEY,
  hash        TEXT NOT NULL UNIQUE,
  owner       TEXT NOT NULL,
  owner_name  TEXT NOT NULL DEFAULT '',
  name        TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  last_used   TEXT,
  revoked_at  TEXT,
  daily_cap   INTEGER NOT NULL,
  revoked_by  TEXT
);
CREATE INDEX IF NOT EXISTS api_keys_owner ON api_keys(owner) WHERE revoked_at IS NULL;
CREATE TABLE IF NOT EXISTS key_usage (
  key_id  TEXT NOT NULL,
  day     TEXT NOT NULL,
  count   INTEGER NOT NULL,
  refused INTEGER NOT NULL DEFAULT 0,
  served  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (key_id, day)
);
CREATE TABLE IF NOT EXISTS key_endpoint_usage (
  key_id   TEXT NOT NULL,
  day      TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  count    INTEGER NOT NULL,
  refused  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (key_id, day, endpoint)
);
CREATE TABLE IF NOT EXISTS blocked_owners (
  owner      TEXT PRIMARY KEY,
  owner_name TEXT NOT NULL,
  blocked_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounts (
  id             TEXT PRIMARY KEY,
  created_at     TEXT NOT NULL,
  tester_seen_at TEXT
);
CREATE TABLE IF NOT EXISTS identities (
  provider   TEXT NOT NULL CHECK (provider IN ('eve', 'discord')),
  subject    TEXT NOT NULL,
  account    TEXT NOT NULL,
  owner_hash TEXT,
  linked_at  TEXT NOT NULL,
  PRIMARY KEY (provider, subject),
  CHECK ((provider = 'eve') = (owner_hash IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS identities_account ON identities(account);
CREATE UNIQUE INDEX IF NOT EXISTS identities_one_discord ON identities(account) WHERE provider = 'discord';`

// Options configures Open.
type Options struct {
	// DailyCap is copied into every new key (0 = no cap).
	DailyCap int
	// MaxPerOwner is the number of active keys one owner may hold (0 means 3).
	MaxPerOwner int
	// Now is the clock (nil: time.Now); tests inject it.
	Now func() time.Time
	// FlushEvery is how often usage counts are written (0 means 30 s).
	FlushEvery time.Duration
	// Logger receives key lifecycle events (nil: discarded).
	Logger *slog.Logger
}

// Key is a stored key without its secret.
type Key struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  string    `json:"last_used,omitempty"` // UTC date YYYY-MM-DD
	DailyCap  int       `json:"daily_cap"`
	UsedToday int       `json:"used_today"` // requests charged against the daily cap
}

// OwnedKey is a Key with its owner, for the operator audit.
type OwnedKey struct {
	Key
	Owner        string `json:"owner"`
	OwnerName    string `json:"owner_name"`
	RefusedToday int    `json:"refused_today"` // requests answered 429 today
	// RevokedAt and RevokedBy are set only for a revoked key (ListRevoked). RevokedBy is the
	// role: RevokerOwner, RevokerAdmin, RevokerOperator or RevokerUnknown.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	RevokedBy string     `json:"revoked_by,omitempty"`
	// Identities are the sign-in identities of an account owner (operator list only; absent for a legacy owner id).
	Identities []LinkedIdentity `json:"identities,omitempty"`
}

// activeKey is what the in-memory index knows of a live key.
type activeKey struct {
	hash string
	cap  int
}

// Store is the key store. It is safe for concurrent use.
type Store struct {
	db  *sql.DB
	opt Options
	now func() time.Time
	log *slog.Logger

	// adminMu serialises Create, SetCap, Block and Unblock (limit check, block check, insert) and every write to
	// accounts, identities and key owners.
	adminMu sync.Mutex

	mu     sync.RWMutex // guards the index
	byHash map[string]string
	byID   map[string]activeKey

	usageMu sync.Mutex // guards the fields below
	day     string
	rows    map[string]usageRow            // key id -> today's totals
	eps     map[string]map[string]usageRow // key id -> endpoint -> today's totals
	dirty   map[string]bool                // key ids whose rows changed since the last flush
	dirtyEp map[string]map[string]bool     // key id -> endpoints changed since the last flush
	pending []usageSnapshot                // snapshots whose write failed and must be retried
	flushMu sync.Mutex                     // serialises writes of usage snapshots

	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// migrate brings a file created by an older version to the current schema. Every step is
// additive, so the previous version still reads and writes the migrated file.
func migrate(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('api_keys') WHERE name = 'revoked_by'`).Scan(&n); err != nil {
		return fmt.Errorf("keys: migrate: %w", err)
	}
	if n == 0 {
		if _, err := db.Exec(`ALTER TABLE api_keys ADD COLUMN revoked_by TEXT`); err != nil {
			return fmt.Errorf("keys: migrate: add revoked_by: %w", err)
		}
	}
	return nil
}

// Open opens (creating if needed) the key database at path, loads the active keys and
// today's usage counts, and starts the usage flusher.
func Open(path string, opt Options) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("keys: open %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("keys: create schema: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{
		db: db, opt: opt, now: opt.Now, log: opt.Logger,
		byHash: map[string]string{}, byID: map[string]activeKey{},
		rows: map[string]usageRow{}, eps: map[string]map[string]usageRow{},
		dirty: map[string]bool{}, dirtyEp: map[string]map[string]bool{},
		done: make(chan struct{}),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.opt.MaxPerOwner <= 0 {
		s.opt.MaxPerOwner = defaultPerUser
	}
	if s.opt.FlushEvery <= 0 {
		s.opt.FlushEvery = defaultFlush
	}
	if err := s.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.flushLoop(ctx)
	return s, nil
}

// MaxPerOwner is the number of active keys one owner may hold.
func (s *Store) MaxPerOwner() int { return s.opt.MaxPerOwner }

func (s *Store) today() string { return s.now().UTC().Format(time.DateOnly) }

func (s *Store) load() error {
	rows, err := s.db.Query(`SELECT id, hash, daily_cap FROM api_keys WHERE revoked_at IS NULL`)
	if err != nil {
		return fmt.Errorf("keys: load keys: %w", err)
	}
	for rows.Next() {
		var id, hash string
		var cap int
		if err := rows.Scan(&id, &hash, &cap); err != nil {
			_ = rows.Close()
			return fmt.Errorf("keys: load keys: %w", err)
		}
		s.byHash[hash] = id
		s.byID[id] = activeKey{hash: hash, cap: cap}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("keys: load keys: %w", err)
	}
	if err := s.loadUsage(); err != nil {
		return err
	}
	if err := s.prune(s.day); err != nil {
		s.log.Error("api_key_usage_prune_failed", "err", err)
	}
	return nil
}

// Close stops the flusher, writes the pending usage counts and closes the database.
// It is safe to call more than once.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
		err := s.flush()
		if cerr := s.db.Close(); err == nil {
			err = cerr
		}
		s.closeErr = err
	})
	return s.closeErr
}

func validateOwner(owner string) error {
	if !ownerRE.MatchString(owner) {
		return ErrBadOwner
	}
	return nil
}

// cleanName trims a key name and checks it: 0-40 runes, no control or format characters. An empty
// name stays empty (the UI shows it as an unnamed key).
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameRunes ||
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return "", ErrBadName
	}
	return name, nil
}

// cleanOwnerName sanitizes an owner display name, which comes from the caller's identity
// provider and so must never make Create fail: invalid UTF-8 becomes U+FFFD,
// control and format characters (bidi overrides, zero-width) are dropped, the result is
// trimmed and cut to 64 runes.
func cleanOwnerName(name string) string {
	name = strings.ToValidUTF8(name, "\uFFFD")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxOwnerRunes {
		name = strings.TrimSpace(string(r[:maxOwnerRunes]))
	}
	return name
}

// isUniqueViolation reports a SQLite UNIQUE or PRIMARY KEY constraint failure.
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	c := se.Code()
	return c == sqlitelib.SQLITE_CONSTRAINT_UNIQUE || c == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("keys: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Create makes a new key for owner and returns it with its secret. The secret is shown
// only here; the store keeps just its hash. ownerName is the owner's display name for
// the operator audit (may be empty). An owner may create MaxCreatesPerDay keys per UTC
// day (ErrCreateLimit), however many of them they revoked. Limits are counted over the whole account when owner
// is an account or a linked sign-in id, whichever id asks;
// ErrNoAccount for an unknown account id.
func (s *Store) Create(ctx context.Context, owner, ownerName, name string) (Key, string, error) {
	return s.create(ctx, "owner", owner, ownerName, name)
}

// Issue is Create for an operator: the key is logged under the actor "operator" and the
// per-day create limit does not apply.
func (s *Store) Issue(ctx context.Context, owner, ownerName, name string) (Key, string, error) {
	return s.create(ctx, "operator", owner, ownerName, name)
}

// CreateLimitRetryAfter is the time until the per-day create counter resets (00:00 UTC).
func (s *Store) CreateLimitRetryAfter() time.Duration {
	now := s.now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return max(next.Sub(now), time.Second)
}

func (s *Store) create(ctx context.Context, actor, owner, ownerName, name string) (Key, string, error) {
	if err := validateOwner(owner); err != nil {
		return Key{}, "", err
	}
	name, err := cleanName(name)
	if err != nil {
		return Key{}, "", err
	}
	ownerName = cleanOwnerName(ownerName)
	s.adminMu.Lock()
	defer s.adminMu.Unlock()

	if accountRE.MatchString(owner) {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id = ?`, owner).Scan(&exists); err != nil {
			return Key{}, "", fmt.Errorf("keys: check account: %w", err)
		}
		if exists == 0 {
			return Key{}, "", ErrNoAccount
		}
	}
	// Blocks and limits cover the whole account (its own rows and its identities' legacy owner ids) for every
	// caller: the new key lands in that set whichever id asked, so a narrower count would let it grow past the caps.
	whole, err := s.ownerScope(ctx, owner, true)
	if err != nil {
		return Key{}, "", err
	}
	in, args := placeholders(len(whole)), anyArgs(whole)
	blocked, err := s.ownersBlocked(ctx, whole)
	if err != nil {
		return Key{}, "", err
	}
	if blocked {
		return Key{}, "", ErrOwnerBlocked
	}
	if actor == "owner" {
		// created_at is RFC3339Nano text, so compare on its date part: a string bound
		// misorders fractional seconds ("00:00:00.5Z" sorts before "00:00:00Z").
		day := s.now().UTC().Format(time.DateOnly)
		var made int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM api_keys WHERE owner IN (`+in+`) AND substr(created_at, 1, 10) = ?`,
			append(append([]any{}, args...), day)...).Scan(&made); err != nil {
			return Key{}, "", fmt.Errorf("keys: count creations: %w", err)
		}
		if made >= MaxCreatesPerDay {
			return Key{}, "", ErrCreateLimit
		}
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys WHERE owner IN (`+in+`) AND revoked_at IS NULL`, args...).Scan(&n); err != nil {
		return Key{}, "", fmt.Errorf("keys: count keys: %w", err)
	}
	if n >= s.opt.MaxPerOwner {
		return Key{}, "", ErrTooManyKeys
	}

	created := s.now().UTC()
	for attempt := 0; ; attempt++ {
		sec, err := randHex(32)
		if err != nil {
			return Key{}, "", err
		}
		idPart, err := randHex(6)
		if err != nil {
			return Key{}, "", err
		}
		secret, id := secretPrefix+sec, "key_"+idPart
		hash := dataapi.HashKey(secret)
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO api_keys(id, hash, owner, owner_name, name, created_at, daily_cap) VALUES(?,?,?,?,?,?,?)`,
			id, hash, owner, ownerName, name, created.Format(time.RFC3339Nano), s.opt.DailyCap)
		if err != nil {
			if attempt == 0 && isUniqueViolation(err) {
				continue // astronomically unlikely collision: draw once more
			}
			return Key{}, "", fmt.Errorf("keys: insert key: %w", err)
		}
		s.mu.Lock()
		s.byHash[hash] = id
		s.byID[id] = activeKey{hash: hash, cap: s.opt.DailyCap}
		s.mu.Unlock()
		s.log.Info("api_key_created", "key_id", id, "actor", actor)
		return Key{ID: id, Name: name, CreatedAt: created, DailyCap: s.opt.DailyCap}, secret, nil
	}
}

// Revoke revokes owner's key id; ErrNotFound when it is not in the owner's scope or already revoked.
func (s *Store) Revoke(ctx context.Context, owner, id string) error {
	if err := validateOwner(owner); err != nil {
		return err
	}
	scope, err := s.ownerScope(ctx, owner, false)
	if err != nil {
		return err
	}
	return s.revoke(ctx, id, `AND owner IN (`+placeholders(len(scope))+`)`, anyArgs(scope), RevokerOwner)
}

// RevokeAny revokes any active key by id (operator). actor is the acting role: "" or
// RevokerOperator for the token holder, RevokerAdmin for an admin in a front end's panel;
// anything else is ErrBadActor. The role is stored as the key's RevokedBy and logged.
func (s *Store) RevokeAny(ctx context.Context, id, actor string) error {
	by, err := operatorActor(actor)
	if err != nil {
		return err
	}
	return s.revoke(ctx, id, "", nil, by)
}

func (s *Store) revoke(ctx context.Context, id, cond string, args []any, actor string) error {
	args = append([]any{s.now().UTC().Format(time.RFC3339Nano), actor, id}, args...)
	var hash string
	err := s.db.QueryRowContext(ctx,
		`UPDATE api_keys SET revoked_at = ?, revoked_by = ? WHERE id = ? AND revoked_at IS NULL `+cond+` RETURNING hash`,
		args...).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("keys: revoke key: %w", err)
	}
	s.mu.Lock()
	delete(s.byHash, hash)
	delete(s.byID, id)
	s.mu.Unlock()
	s.log.Info("api_key_revoked", "key_id", id, "actor", actor)
	return nil
}

// List returns owner's active keys, newest first, with today's usage, including those under the legacy owner ids
// of an account's identities.
func (s *Store) List(ctx context.Context, owner string) ([]Key, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	scope, err := s.ownerScope(ctx, owner, false)
	if err != nil {
		return nil, err
	}
	all, err := s.query(ctx, `WHERE revoked_at IS NULL AND owner IN (`+placeholders(len(scope))+`)`, anyArgs(scope)...)
	if err != nil {
		return nil, err
	}
	out := make([]Key, len(all))
	for i, k := range all {
		out[i] = k.Key
	}
	return out, nil
}

func (s *Store) query(ctx context.Context, where string, args ...any) ([]OwnedKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, owner, owner_name, name, created_at, last_used, daily_cap, revoked_at, revoked_by FROM api_keys `+where+
			` ORDER BY rowid DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("keys: list keys: %w", err)
	}
	defer rows.Close()
	var out []OwnedKey
	for rows.Next() {
		var k OwnedKey
		var created string
		var last, revokedAt, revokedBy sql.NullString
		if err := rows.Scan(&k.ID, &k.Owner, &k.OwnerName, &k.Name, &created, &last, &k.DailyCap, &revokedAt, &revokedBy); err != nil {
			return nil, fmt.Errorf("keys: list keys: %w", err)
		}
		if k.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("keys: parse created_at: %w", err)
		}
		k.LastUsed = last.String
		if revokedAt.Valid {
			at, err := time.Parse(time.RFC3339Nano, revokedAt.String)
			if err != nil {
				return nil, fmt.Errorf("keys: parse revoked_at: %w", err)
			}
			k.RevokedAt, k.RevokedBy = &at, revokedBy.String
			if k.RevokedBy == "" {
				k.RevokedBy = RevokerUnknown
			}
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("keys: list keys: %w", err)
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	day := s.today()
	if day < s.day { // the clock stepped back: show the day the counters are on
		day = s.day
	}
	for i := range out {
		if s.day == day {
			r := s.rows[out[i].ID]
			out[i].UsedToday, out[i].RefusedToday = r.count, r.refused
		}
		if out[i].UsedToday > 0 {
			out[i].LastUsed = day
		}
	}
	return out, nil
}

// Authenticate implements dataapi.Authenticator: no key is anonymous; a key this store
// does not hold (including any key without the ecd_ prefix, so a key file can answer
// for it) is dataapi.ErrInvalidAPIKey.
func (s *Store) Authenticate(r *http.Request) (dataapi.Caller, error) {
	key, present := dataapi.APIKeyFrom(r)
	if !present {
		return dataapi.Caller{}, nil
	}
	if !strings.HasPrefix(key, secretPrefix) {
		return dataapi.Caller{}, dataapi.ErrInvalidAPIKey
	}
	s.mu.RLock()
	id, ok := s.byHash[dataapi.HashKey(key)]
	s.mu.RUnlock()
	if !ok {
		return dataapi.Caller{}, dataapi.ErrInvalidAPIKey
	}
	return dataapi.NewCaller(id, catalog.TierKeyed), nil
}
