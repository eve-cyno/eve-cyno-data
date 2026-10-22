package keys

import (
	"cmp"
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

// MaxRevokedListed caps ListRevoked: the web server reads a bounded admin response, so a
// longer list would be cut. The newest revocations are the ones kept. The operator list also
// cuts by size (MaxOpsListBytes).
const MaxRevokedListed = 1000

// ListRevoked returns the keys revoked in the last 30 days (the rows the store still keeps),
// most recently revoked first (keys revoked at the same instant: the newest-inserted first),
// with RevokedAt, RevokedBy and the fields ListAll returns (operator audit). At most
// MaxRevokedListed rows come back, the first in that order.
func (s *Store) ListRevoked(ctx context.Context) ([]OwnedKey, error) {
	ks, _, err := s.listRevoked(ctx)
	return ks, err
}

// listRevoked is ListRevoked plus whether older rows were left out of the list.
func (s *Store) listRevoked(ctx context.Context) (ks []OwnedKey, capped bool, err error) {
	ks, err = s.query(ctx, `WHERE revoked_at IS NOT NULL AND revoked_at >= ?`, revokedCutoff(s.now().UTC()))
	if err != nil {
		return nil, false, err
	}
	// query returns the rows newest-inserted first (rowid DESC); that position is the second
	// key, so equal revocation times keep the newest-inserted key, also at the cap.
	pos := make(map[string]int, len(ks))
	for i, k := range ks {
		pos[k.ID] = i
	}
	slices.SortFunc(ks, func(a, b OwnedKey) int {
		if c := b.RevokedAt.Compare(*a.RevokedAt); c != 0 {
			return c
		}
		return cmp.Compare(pos[a.ID], pos[b.ID])
	})
	if len(ks) > MaxRevokedListed {
		return ks[:MaxRevokedListed], true, nil
	}
	return ks, false, nil
}
