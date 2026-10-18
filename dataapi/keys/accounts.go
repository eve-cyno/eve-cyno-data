package keys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sign-in providers an identity can come from.
const (
	ProviderEVE     = "eve"
	ProviderDiscord = "discord"
)

// AccountPattern is the shape of a canonical account id: "acct:" and 16 lowercase hex digits.
const AccountPattern = `^acct:[0-9a-f]{16}$`

// TesterValidity is how long Discord tester evidence keeps an account a tester. Every Discord sign-in or bot
// interaction that carries the evidence refreshes it.
const TesterValidity = 7 * 24 * time.Hour

// storedTime is the fixed-width layout of stored timestamps, so their text order is their time order. It parses
// as RFC3339Nano.
const storedTime = "2006-01-02T15:04:05.000000000Z07:00"

var (
	accountRE   = regexp.MustCompile(AccountPattern)
	subjectRE   = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	ownerHashRE = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{1,64}$`)
)

var (
	// ErrBadIdentity: the provider is not eve or discord, the subject is not a decimal id that fits 64 bits, the
	// owner hash is malformed or given for a Discord identity, or tester evidence is given for an EVE identity.
	ErrBadIdentity = errors.New("invalid identity")
	// ErrIdentityChanged: an EVE identity came without an owner hash, or, outside a fresh sign-in, with one that is not
	// the stored one (the character changed hands). The user has to sign in again.
	ErrIdentityChanged = errors.New("identity changed hands or carries no owner hash")
	// ErrNoAccount: a key request named an account id the store does not hold.
	ErrNoAccount = errors.New("no such account")
)

// Identity is one sign-in identity: an EVE character or a Discord user. OwnerHash is the EVE owner hash of the
// sign-in (EVE only).
type Identity struct {
	Provider  string `json:"provider"`
	Subject   string `json:"subject"`
	OwnerHash string `json:"owner_hash,omitempty"`
}

// LinkedIdentity is an identity attached to an account. The owner hash is never returned.
type LinkedIdentity struct {
	Provider string    `json:"provider"`
	Subject  string    `json:"subject"`
	LinkedAt time.Time `json:"linked_at"`
}

// Account is a canonical account with its identities, oldest link first. Tester is true while the account carries
// Discord tester evidence younger than TesterValidity; TesterExpired is true when it carries older evidence (the UI
// then asks for a Discord sign-in to refresh it).
type Account struct {
	ID            string           `json:"account"`
	Identities    []LinkedIdentity `json:"identities"`
	Tester        bool             `json:"tester"`
	TesterExpired bool             `json:"tester_expired,omitempty"`
	// Blocked is true while the account, or a legacy owner id of one of its identities, is on the block list. Its
	// owner can then neither change nor delete it.
	Blocked bool `json:"blocked,omitempty"`
}

// ResolveOptions qualifies a Resolve.
type ResolveOptions struct {
	// SignIn says the identity comes from a fresh, verified sign-in.
	SignIn bool
	// Tester is Discord tester evidence from that sign-in or from a bot interaction: true marks the account a tester
	// now, false clears the mark, nil leaves it. Discord identities only.
	Tester *bool
}

// LogValue keeps the owner hash out of any log line that carries an Identity.
func (id Identity) LogValue() slog.Value {
	return slog.GroupValue(slog.String("provider", id.Provider), slog.String("subject", id.Subject))
}

// validate checks the shape of an identity (the owner hash is optional here).
func (id Identity) validate() error {
	switch id.Provider {
	case ProviderEVE:
		if id.OwnerHash != "" && !ownerHashRE.MatchString(id.OwnerHash) {
			return ErrBadIdentity
		}
	case ProviderDiscord:
		if id.OwnerHash != "" {
			return ErrBadIdentity
		}
	default:
		return ErrBadIdentity
	}
	if !subjectRE.MatchString(id.Subject) {
		return ErrBadIdentity
	}
	if _, err := strconv.ParseUint(id.Subject, 10, 64); err != nil {
		return ErrBadIdentity
	}
	return nil
}

// signedIn checks an identity that stands for a sign-in: well formed, and an EVE one carries its owner hash.
func (id Identity) signedIn() error {
	if err := id.validate(); err != nil {
		return err
	}
	if id.Provider == ProviderEVE && id.OwnerHash == "" {
		return ErrIdentityChanged
	}
	return nil
}

// legacyOwner is the owner id keys carried for this identity before accounts existed.
func (id Identity) legacyOwner() string { return id.Provider + ":" + id.Subject }

// identityRow is what the store holds for one identity.
type identityRow struct {
	account   string
	ownerHash string
	found     bool
}

func (s *Store) lookupIdentity(ctx context.Context, id Identity) (identityRow, error) {
	var r identityRow
	var hash sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT account, owner_hash FROM identities WHERE provider = ? AND subject = ?`,
		id.Provider, id.Subject).Scan(&r.account, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return identityRow{}, nil
	}
	if err != nil {
		return identityRow{}, fmt.Errorf("keys: look up identity: %w", err)
	}
	r.ownerHash, r.found = hash.String, true
	return r, nil
}

// Resolve returns the account of id, creating one for an identity seen for the first time. An EVE identity must
// carry its owner hash (else ErrIdentityChanged); every stored EVE identity has one. When the given hash differs from
// the stored one, a fresh sign-in (opt.SignIn) moves the character to a new, empty account, because it changed hands,
// and what the old account holds stays there. Any other call is then ErrIdentityChanged and changes nothing.
// opt.Tester records Discord tester evidence on the account.
func (s *Store) Resolve(ctx context.Context, id Identity, opt ResolveOptions) (Account, error) {
	if err := id.signedIn(); err != nil {
		return Account{}, err
	}
	if opt.Tester != nil && id.Provider != ProviderDiscord {
		return Account{}, ErrBadIdentity
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	acct, err := s.resolveLocked(ctx, id, opt.SignIn)
	if err == nil && opt.Tester != nil {
		err = s.markTester(ctx, acct, *opt.Tester)
	}
	if err != nil {
		return Account{}, err
	}
	// Loaded under the lock: a concurrent re-home cannot drop the account between resolve and load.
	return s.loadAccount(ctx, acct)
}

// resolveLocked is Resolve for a checked id. The caller holds adminMu, which serialises every write to accounts,
// identities and key owners (the usage flusher writes none of them).
func (s *Store) resolveLocked(ctx context.Context, id Identity, signIn bool) (string, error) {
	row, err := s.lookupIdentity(ctx, id)
	if err != nil {
		return "", err
	}
	switch {
	case !row.found:
		return s.attachToNewAccount(ctx, id, "")
	case id.Provider == ProviderEVE && row.ownerHash != id.OwnerHash:
		if !signIn {
			return "", ErrIdentityChanged
		}
		return s.attachToNewAccount(ctx, id, row.account)
	}
	return row.account, nil
}

// markTester records Discord tester evidence on account acct: the current time, or none.
func (s *Store) markTester(ctx context.Context, acct string, tester bool) error {
	var at any
	if tester {
		at = s.now().UTC().Format(storedTime)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET tester_seen_at = ? WHERE id = ?`, at, acct); err != nil {
		return fmt.Errorf("keys: record tester evidence: %w", err)
	}
	return nil
}

// attachToNewAccount creates an account and attaches id to it. prev, when set, is the account the identity held
// before (an EVE character that changed hands): the keys and the block still recorded under the identity's legacy
// owner id stay with prev, and prev is deleted only when nothing refers to it any more.
func (s *Store) attachToNewAccount(ctx context.Context, id Identity, prev string) (string, error) {
	now := s.now().UTC().Format(storedTime)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("keys: new account: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	acct, err := insertAccount(ctx, tx, now) // a write first: see Block on BUSY_SNAPSHOT
	if err != nil {
		return "", err
	}
	if prev != "" {
		if err := detachLegacy(ctx, tx, id.legacyOwner(), prev); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM identities WHERE provider = ? AND subject = ?`, id.Provider, id.Subject); err != nil {
			return "", fmt.Errorf("keys: new account: %w", err)
		}
		if err := dropIfEmpty(ctx, tx, prev); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identities(provider, subject, account, owner_hash, linked_at) VALUES(?,?,?,?,?)`,
		id.Provider, id.Subject, acct, nullable(id.OwnerHash), now); err != nil {
		return "", fmt.Errorf("keys: new account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("keys: new account: %w", err)
	}
	if prev != "" {
		s.log.Info("account_identity_rehomed", "provider", id.Provider)
	} else {
		s.log.Info("account_created", "provider", id.Provider)
	}
	return acct, nil
}

// insertAccount adds an account row with a fresh random id and returns the id.
func insertAccount(ctx context.Context, tx *sql.Tx, now string) (string, error) {
	for attempt := 0; ; attempt++ {
		h, err := randHex(8)
		if err != nil {
			return "", err
		}
		id := "acct:" + h
		_, err = tx.ExecContext(ctx, `INSERT INTO accounts(id, created_at) VALUES(?, ?)`, id, now)
		if err == nil {
			return id, nil
		}
		if attempt == 0 && isUniqueViolation(err) {
			continue // astronomically unlikely collision: draw once more
		}
		return "", fmt.Errorf("keys: insert account: %w", err)
	}
}

// detachLegacy moves the key rows and the block still recorded under a legacy owner id to account to, so they stay
// with the account the identity leaves.
func detachLegacy(ctx context.Context, tx *sql.Tx, legacy, to string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET owner = ? WHERE owner = ?`, to, legacy); err != nil {
		return fmt.Errorf("keys: move legacy keys: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO blocked_owners(owner, owner_name, blocked_at)
		 SELECT ?, owner_name, blocked_at FROM blocked_owners WHERE owner = ?
		 ON CONFLICT(owner) DO NOTHING`, to, legacy); err != nil {
		return fmt.Errorf("keys: move legacy block: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blocked_owners WHERE owner = ?`, legacy); err != nil {
		return fmt.Errorf("keys: move legacy block: %w", err)
	}
	return nil
}

// dropIfEmpty deletes account acct when no identity, key row or block refers to it.
func dropIfEmpty(ctx context.Context, tx *sql.Tx, acct string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM accounts WHERE id = ?
		   AND NOT EXISTS (SELECT 1 FROM identities WHERE account = ?)
		   AND NOT EXISTS (SELECT 1 FROM api_keys WHERE owner = ?)
		   AND NOT EXISTS (SELECT 1 FROM blocked_owners WHERE owner = ?)`, acct, acct, acct, acct); err != nil {
		return fmt.Errorf("keys: drop empty account: %w", err)
	}
	return nil
}

func (s *Store) loadAccount(ctx context.Context, acct string) (Account, error) {
	m, err := s.IdentitiesOf(ctx, []string{acct})
	if err != nil {
		return Account{}, err
	}
	a := Account{ID: acct, Identities: m[acct]}
	if a.Identities == nil {
		a.Identities = []LinkedIdentity{}
	}
	var seen sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT tester_seen_at FROM accounts WHERE id = ?`, acct).Scan(&seen)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNoAccount
	}
	if err != nil {
		return Account{}, fmt.Errorf("keys: load account: %w", err)
	}
	if seen.Valid {
		at, err := time.Parse(time.RFC3339Nano, seen.String)
		if err != nil {
			return Account{}, fmt.Errorf("keys: parse tester_seen_at: %w", err)
		}
		a.Tester = s.now().Sub(at) <= TesterValidity
		a.TesterExpired = !a.Tester
	}
	set, err := s.ownerSet(ctx, acct)
	if err != nil {
		return Account{}, err
	}
	if a.Blocked, err = s.ownersBlocked(ctx, set); err != nil {
		return Account{}, err
	}
	return a, nil
}

// IdentitiesOf returns the identities of each account id in accounts, oldest link first. Ids that are not account
// ids are skipped, and accounts without identities are absent from the map.
func (s *Store) IdentitiesOf(ctx context.Context, accounts []string) (map[string][]LinkedIdentity, error) {
	seen := map[string]bool{}
	var ids []string
	for _, a := range accounts {
		if accountRE.MatchString(a) && !seen[a] {
			seen[a] = true
			ids = append(ids, a)
		}
	}
	out := map[string][]LinkedIdentity{}
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		rows, err := s.db.QueryContext(ctx,
			`SELECT account, provider, subject, linked_at FROM identities WHERE account IN (`+placeholders(len(chunk))+`)
			 ORDER BY linked_at, provider, subject`, anyArgs(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("keys: list identities: %w", err)
		}
		for rows.Next() {
			var acct, at string
			var li LinkedIdentity
			if err := rows.Scan(&acct, &li.Provider, &li.Subject, &at); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("keys: list identities: %w", err)
			}
			if li.LinkedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("keys: parse linked_at: %w", err)
			}
			out[acct] = append(out[acct], li)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("keys: list identities: %w", err)
		}
		_ = rows.Close()
	}
	return out, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// placeholders returns "?,?,…" for n bound values (n >= 1).
func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anyArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, v := range ss {
		out[i] = v
	}
	return out
}
