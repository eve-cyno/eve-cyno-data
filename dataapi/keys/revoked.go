package keys

import (
	"context"
	"errors"
	"slices"
	"time"
)

// Values of OwnedKey.RevokedBy: the role that revoked a key, never who.
const (
	// RevokerOwner: the key's owner revoked it.
	RevokerOwner = "owner"
	// RevokerAdmin: an admin revoked it through a front end's admin panel (operator API, X-Actor: admin).
	RevokerAdmin = "admin"
	// RevokerOperator: revoked through the operator API with no admin panel involved (the token holder).
	RevokerOperator = "operator"
	// RevokerUnknown: revoked before the store recorded the role.
	RevokerUnknown = "unknown"
)

// ErrBadActor is returned by RevokeAny and Block for an actor other than "",
// RevokerOperator and RevokerAdmin.
var ErrBadActor = errors.New("invalid actor")

// operatorActor checks the role of an operator revoke or block; "" means RevokerOperator.
func operatorActor(actor string) (string, error) {
	switch actor {
	case "", RevokerOperator:
		return RevokerOperator, nil
	case RevokerAdmin:
		return RevokerAdmin, nil
	}
	return "", ErrBadActor
}

// revokedCutoff is the oldest revocation date (YYYY-MM-DD) kept on day end: keys revoked
// before it are pruned (pruneRevoked) and no longer listed (ListRevoked). revoked_at is
// RFC 3339 text, so a key revoked on the cutoff day itself compares >= and is kept.
func revokedCutoff(end time.Time) string {
	return end.AddDate(0, 0, -revokedRetainDays).Format(time.DateOnly)
}

// ListRevoked returns the keys revoked in the last 30 days (the rows the store still keeps),
// most recently revoked first, with RevokedAt, RevokedBy and the fields ListAll returns
// (operator audit).
func (s *Store) ListRevoked(ctx context.Context) ([]OwnedKey, error) {
	ks, err := s.query(ctx, `WHERE revoked_at IS NOT NULL AND revoked_at >= ?`, revokedCutoff(s.now().UTC()))
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(ks, func(a, b OwnedKey) int { return b.RevokedAt.Compare(*a.RevokedAt) })
	return ks, nil
}
