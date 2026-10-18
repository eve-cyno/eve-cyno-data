package keys

import (
	"context"
	"fmt"
)

// DeletedAccount is what a deletion removed: the number of key rows and the identities that were attached.
type DeletedAccount struct {
	KeysDeleted int              `json:"keys_deleted"`
	Identities  []LinkedIdentity `json:"identities"`
}

// DeleteAccountOf deletes the account of current, the identity the caller is signed in with (self-service). It
// removes, in one transaction:
//   - the account row;
//   - its identities (ids, owner hashes, link dates);
//   - every key row of its owner set (the account and the legacy owner ids of its identities), active or revoked,
//     with their usage and endpoint usage rows.
//
// The keys stop authenticating at once, and their in-memory counters are dropped. An EVE identity must carry the
// stored owner hash, else ErrIdentityChanged: a session from before the character changed hands cannot delete the
// new owner's account. ErrNoAccount when the identity belongs to no account (none is created). ErrOwnerBlocked, with
// nothing changed, when the account or a legacy owner id of one of its identities is blocked. Nothing about a
// deleted account is kept; an identity that signs in again starts a new, empty account. The log line carries no id.
func (s *Store) DeleteAccountOf(ctx context.Context, current Identity) (DeletedAccount, error) {
	if err := current.signedIn(); err != nil {
		return DeletedAccount{}, err
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	s.flushMu.Lock() // no usage write may re-insert rows while they are deleted
	defer s.flushMu.Unlock()
	row, err := s.lookupIdentity(ctx, current)
	if err != nil {
		return DeletedAccount{}, err
	}
	if !row.found {
		return DeletedAccount{}, ErrNoAccount
	}
	if current.Provider == ProviderEVE && row.ownerHash != current.OwnerHash {
		return DeletedAccount{}, ErrIdentityChanged
	}
	return s.deleteAccountLocked(ctx, row.account, false)
}

// DeleteAccount is the operator's deletion of account acct: the same rows as DeleteAccountOf. A blocked account is
// deleted too, but its block is kept on every sign-in id of the account, so the person is still blocked when they
// sign in again. ErrNoAccount when acct is not an account the store holds.
func (s *Store) DeleteAccount(ctx context.Context, acct string) (DeletedAccount, error) {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	return s.deleteAccountLocked(ctx, acct, true)
}

// deleteAccountLocked deletes account acct. A blocked account is refused unless operator is set, which keeps the
// block on its sign-in ids instead. The caller holds adminMu (no link, unlink, block or create runs meanwhile) and
// flushMu.
func (s *Store) deleteAccountLocked(ctx context.Context, acct string, operator bool) (DeletedAccount, error) {
	if !accountRE.MatchString(acct) {
		return DeletedAccount{}, ErrNoAccount
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id = ?`, acct).Scan(&exists); err != nil {
		return DeletedAccount{}, fmt.Errorf("keys: delete account: %w", err)
	}
	if exists == 0 {
		return DeletedAccount{}, ErrNoAccount
	}
	set, err := s.ownerSet(ctx, acct)
	if err != nil {
		return DeletedAccount{}, err
	}
	blocked, err := s.ownersBlocked(ctx, set)
	if err != nil {
		return DeletedAccount{}, err
	}
	if blocked && !operator {
		return DeletedAccount{}, ErrOwnerBlocked
	}
	m, err := s.IdentitiesOf(ctx, []string{acct})
	if err != nil {
		return DeletedAccount{}, err
	}
	out := DeletedAccount{Identities: m[acct]}
	if out.Identities == nil {
		out.Identities = []LinkedIdentity{}
	}
	in, args := placeholders(len(set)), anyArgs(set)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeletedAccount{}, fmt.Errorf("keys: delete account: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Write first (see Block): a read-then-write deferred transaction can fail with BUSY_SNAPSHOT.
	if blocked {
		// Operator path only: keep the block on every sign-in id (the oldest entry's name and time), then drop the
		// entry recorded on the account id itself, which nothing can reach after the deletion.
		for _, li := range out.Identities {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO blocked_owners(owner, owner_name, blocked_at)
				 SELECT ?, owner_name, blocked_at FROM blocked_owners WHERE owner IN (`+in+`)
				 ORDER BY blocked_at LIMIT 1
				 ON CONFLICT(owner) DO NOTHING`,
				append([]any{li.Provider + ":" + li.Subject}, args...)...); err != nil {
				return DeletedAccount{}, fmt.Errorf("keys: delete account: keep block: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM blocked_owners WHERE owner = ?`, acct); err != nil {
			return DeletedAccount{}, fmt.Errorf("keys: delete account: keep block: %w", err)
		}
	}
	for _, q := range []string{
		`DELETE FROM key_usage WHERE key_id IN (SELECT id FROM api_keys WHERE owner IN (` + in + `))`,
		`DELETE FROM key_endpoint_usage WHERE key_id IN (SELECT id FROM api_keys WHERE owner IN (` + in + `))`,
	} {
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return DeletedAccount{}, fmt.Errorf("keys: delete account: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `DELETE FROM api_keys WHERE owner IN (`+in+`) RETURNING id, hash`, args...)
	if err != nil {
		return DeletedAccount{}, fmt.Errorf("keys: delete account: %w", err)
	}
	gone, err := scanIDHashes(rows)
	if err != nil {
		return DeletedAccount{}, fmt.Errorf("keys: delete account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM identities WHERE account = ?`, acct); err != nil {
		return DeletedAccount{}, fmt.Errorf("keys: delete account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, acct); err != nil {
		return DeletedAccount{}, fmt.Errorf("keys: delete account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DeletedAccount{}, fmt.Errorf("keys: delete account: %w", err)
	}
	s.unindex(gone)
	s.dropCounters(gone)
	out.KeysDeleted = len(gone)
	s.log.Info("account_deleted", "keys_deleted", len(gone), "block_kept", blocked)
	return out, nil
}
