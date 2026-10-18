package keys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	// ErrDiscordConflict: the account would hold two Discord identities.
	ErrDiscordConflict = errors.New("account already has another Discord identity")
	// ErrNotLinked: the identity to unlink is not attached to the caller's account.
	ErrNotLinked = errors.New("identity is not linked to this account")
	// ErrCurrentIdentity: the identity to unlink is the one the caller is signed in with.
	ErrCurrentIdentity = errors.New("the identity in use cannot be unlinked")
)

// Link attaches add, a freshly verified sign-in, to the account of current (the identity the caller is signed in
// with). It returns that account and the ids of the accounts merged into it (empty, never nil).
//
//   - add is new: it joins the account.
//   - add is already in the account: an EVE owner hash that differs is replaced (the fresh login proves ownership).
//   - add is an EVE character in another account with a different owner hash: it changed hands. Only the identity
//     moves; its previous account keeps what it holds and is not merged.
//   - add belongs to another account: that account merges in. Its identities and every key row it owns (active and
//     revoked) move, its tester evidence counts if it is newer, and it is deleted. A merge never revokes: an account
//     above the per-owner key limit keeps every key, and Create refuses new ones until it is under the limit. Today's
//     creations add up because Create counts rows.
//
// The caller guarantees where the arguments come from. add comes only from the identity verified in this link's own
// OAuth callback (EVE: the character id and owner hash of a verified token; Discord: the user from /users/@me with the
// code-exchange token), never from client input. The OAuth state is single-use, short-lived and bound server-side to
// the session that started the link. current comes from the server-verified session.
//
// Refusals change nothing: ErrBadIdentity, ErrIdentityChanged (an EVE side without its owner hash, or a stale
// current), ErrOwnerBlocked (either account is blocked), ErrDiscordConflict (the result would hold two Discord
// identities).
func (s *Store) Link(ctx context.Context, current, add Identity) (Account, []string, error) {
	if err := current.signedIn(); err != nil {
		return Account{}, nil, err
	}
	if err := add.signedIn(); err != nil {
		return Account{}, nil, err
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	acct, merged, err := s.linkLocked(ctx, current, add)
	if err != nil {
		return Account{}, nil, err
	}
	// Loaded under the lock, like Resolve: a concurrent merge cannot drop the account before it is read.
	a, err := s.loadAccount(ctx, acct)
	if err != nil {
		return Account{}, nil, err
	}
	return a, merged, nil
}

func (s *Store) linkLocked(ctx context.Context, current, add Identity) (string, []string, error) {
	acct, err := s.resolveLocked(ctx, current, false)
	if err != nil {
		return "", nil, err
	}
	if err := s.refuseBlocked(ctx, acct); err != nil {
		return "", nil, err
	}
	row, err := s.lookupIdentity(ctx, add)
	if err != nil {
		return "", nil, err
	}
	now := s.now().UTC().Format(storedTime)
	switch {
	case row.found && row.account == acct:
		if add.Provider == ProviderEVE && row.ownerHash != add.OwnerHash {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE identities SET owner_hash = ? WHERE provider = ? AND subject = ?`,
				add.OwnerHash, add.Provider, add.Subject); err != nil {
				return "", nil, fmt.Errorf("keys: link: %w", err)
			}
			s.log.Info("account_identity_rehashed", "provider", add.Provider)
		}
		return acct, []string{}, nil
	case row.found && add.Provider == ProviderEVE && row.ownerHash != add.OwnerHash:
		if err := s.moveIdentity(ctx, add, row.account, acct, now); err != nil {
			return "", nil, err
		}
		return acct, []string{}, nil
	case row.found:
		return s.mergeLocked(ctx, acct, row.account, now)
	}
	// A never-resolved identity can still carry a block recorded under its legacy owner id.
	if blocked, err := s.ownersBlocked(ctx, []string{add.legacyOwner()}); err != nil {
		return "", nil, err
	} else if blocked {
		return "", nil, ErrOwnerBlocked
	}
	if add.Provider == ProviderDiscord {
		d, err := s.discordOf(ctx, acct)
		if err != nil {
			return "", nil, err
		}
		if d != "" {
			return "", nil, ErrDiscordConflict
		}
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO identities(provider, subject, account, owner_hash, linked_at) VALUES(?,?,?,?,?)`,
		add.Provider, add.Subject, acct, nullable(add.OwnerHash), now); err != nil {
		return "", nil, fmt.Errorf("keys: link: %w", err)
	}
	s.log.Info("account_linked", "provider", add.Provider)
	return acct, []string{}, nil
}

// moveIdentity moves an EVE character that changed hands from account prev to account to. What prev holds under the
// character's legacy owner id stays with prev.
func (s *Store) moveIdentity(ctx context.Context, id Identity, prev, to, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("keys: move identity: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := detachLegacy(ctx, tx, id.legacyOwner(), prev); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE identities SET account = ?, owner_hash = ?, linked_at = ? WHERE provider = ? AND subject = ?`,
		to, id.OwnerHash, now, id.Provider, id.Subject); err != nil {
		return fmt.Errorf("keys: move identity: %w", err)
	}
	if err := dropIfEmpty(ctx, tx, prev); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("keys: move identity: %w", err)
	}
	s.log.Info("account_identity_rehomed", "provider", id.Provider)
	return nil
}

// mergeLocked folds account other into acct (see Link). The caller holds adminMu.
func (s *Store) mergeLocked(ctx context.Context, acct, other, now string) (string, []string, error) {
	if err := s.refuseBlocked(ctx, other); err != nil {
		return "", nil, err
	}
	dA, err := s.discordOf(ctx, acct)
	if err != nil {
		return "", nil, err
	}
	dB, err := s.discordOf(ctx, other)
	if err != nil {
		return "", nil, err
	}
	if dA != "" && dB != "" {
		return "", nil, ErrDiscordConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, fmt.Errorf("keys: merge accounts: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, st := range []struct {
		q    string
		args []any
	}{
		{`UPDATE identities SET account = ?, linked_at = ? WHERE account = ?`, []any{acct, now, other}},
		{`UPDATE accounts SET tester_seen_at = (SELECT MAX(tester_seen_at) FROM accounts WHERE id IN (?, ?)) WHERE id = ?`,
			[]any{acct, other, acct}},
		{`UPDATE api_keys SET owner = ? WHERE owner = ?`, []any{acct, other}},
		{`DELETE FROM accounts WHERE id = ?`, []any{other}},
	} {
		if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
			return "", nil, fmt.Errorf("keys: merge accounts: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("keys: merge accounts: %w", err)
	}
	s.log.Info("account_merged")
	return acct, []string{other}, nil
}

// refuseBlocked is ErrOwnerBlocked when account acct, or a legacy owner id of one of its identities, is blocked.
func (s *Store) refuseBlocked(ctx context.Context, acct string) error {
	set, err := s.ownerSet(ctx, acct)
	if err != nil {
		return err
	}
	blocked, err := s.ownersBlocked(ctx, set)
	if err != nil {
		return err
	}
	if blocked {
		return ErrOwnerBlocked
	}
	return nil
}

// discordOf returns the Discord user id linked to account acct, or "".
func (s *Store) discordOf(ctx context.Context, acct string) (string, error) {
	var sub string
	err := s.db.QueryRowContext(ctx,
		`SELECT subject FROM identities WHERE account = ? AND provider = ?`, acct, ProviderDiscord).Scan(&sub)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("keys: discord identity: %w", err)
	}
	return sub, nil
}

// Unlink detaches the identity provider/subject from the account of current. The account keeps its keys and trial
// counters, including keys still recorded under the identity's legacy owner id. Unlinking the Discord login also
// clears the account's tester mark, which came from it. The detached identity gets a new, empty account (no tester
// mark) the next time it is resolved. Refusals change nothing: ErrBadIdentity, ErrIdentityChanged (current is stale
// or has no owner hash), ErrCurrentIdentity (the identity in use), ErrNotLinked, ErrOwnerBlocked (a blocked account
// cannot shed an identity).
func (s *Store) Unlink(ctx context.Context, current Identity, provider, subject string) (Account, error) {
	target := Identity{Provider: provider, Subject: subject}
	if err := current.signedIn(); err != nil {
		return Account{}, err
	}
	if err := target.validate(); err != nil {
		return Account{}, err
	}
	if target.Provider == current.Provider && target.Subject == current.Subject {
		return Account{}, ErrCurrentIdentity
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	acct, err := s.unlinkLocked(ctx, current, target)
	if err != nil {
		return Account{}, err
	}
	return s.loadAccount(ctx, acct)
}

func (s *Store) unlinkLocked(ctx context.Context, current, target Identity) (string, error) {
	acct, err := s.resolveLocked(ctx, current, false)
	if err != nil {
		return "", err
	}
	row, err := s.lookupIdentity(ctx, target)
	if err != nil {
		return "", err
	}
	if !row.found || row.account != acct {
		return "", ErrNotLinked
	}
	if err := s.refuseBlocked(ctx, acct); err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("keys: unlink: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := detachLegacy(ctx, tx, target.legacyOwner(), acct); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM identities WHERE provider = ? AND subject = ?`, target.Provider, target.Subject); err != nil {
		return "", fmt.Errorf("keys: unlink: %w", err)
	}
	if target.Provider == ProviderDiscord {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET tester_seen_at = NULL WHERE id = ?`, acct); err != nil {
			return "", fmt.Errorf("keys: unlink: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("keys: unlink: %w", err)
	}
	s.log.Info("account_unlinked", "provider", target.Provider)
	return acct, nil
}
