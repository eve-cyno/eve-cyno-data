package keys

import (
	"context"
	"fmt"
	"strings"
)

// ownerSet lists the owner ids whose keys belong to account acct: the account itself and the legacy owner ids of
// its identities (keys created before accounts existed keep those).
func (s *Store) ownerSet(ctx context.Context, acct string) ([]string, error) {
	set := []string{acct}
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, subject FROM identities WHERE account = ? ORDER BY provider, subject`, acct)
	if err != nil {
		return nil, fmt.Errorf("keys: owner set: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p, sub string
		if err := rows.Scan(&p, &sub); err != nil {
			return nil, fmt.Errorf("keys: owner set: %w", err)
		}
		set = append(set, p+":"+sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("keys: owner set: %w", err)
	}
	return set, nil
}

// ownerScope is the set of owner ids a key request for owner acts on. For an account id it is its owner set, and
// so it is for a Discord id that names a linked identity (Discord ids cannot change hands). An "eve:<id>" owner is
// the exception: a character can change hands, and the caller holds no owner hash here, so it would reach the
// account of whoever holds the character now. It stays exactly itself, unless operator is set (the operator's
// own paths, Block and Issue), where the caller is trusted and the legacy id expands to its account like the others.
// Any other owner id stands alone. Legacy ids are only ever mapped through an existing identity row.
func (s *Store) ownerScope(ctx context.Context, owner string, operator bool) ([]string, error) {
	if accountRE.MatchString(owner) {
		return s.ownerSet(ctx, owner)
	}
	p, sub, _ := strings.Cut(owner, ":")
	if p == ProviderEVE && !operator {
		return []string{owner}, nil
	}
	id := Identity{Provider: p, Subject: sub}
	if id.validate() != nil {
		return []string{owner}, nil
	}
	row, err := s.lookupIdentity(ctx, id)
	if err != nil {
		return nil, err
	}
	if !row.found {
		return []string{owner}, nil
	}
	return s.ownerSet(ctx, row.account)
}

// ownersBlocked reports whether any owner id in set is on the block list.
func (s *Store) ownersBlocked(ctx context.Context, set []string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM blocked_owners WHERE owner IN (`+placeholders(len(set))+`)`, anyArgs(set)...).Scan(&n); err != nil {
		return false, fmt.Errorf("keys: check block: %w", err)
	}
	return n > 0, nil
}
