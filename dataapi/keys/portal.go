package keys

import (
	"context"
	"errors"

	"eve-cyno.dev/go/data/dataapi"
)

// errDiscordLinked refuses the portal's deletion of an account that has a Discord login: that login also has a user
// in the chat's identity provider, which only the chat can delete.
var errDiscordLinked = errors.New("account has a Discord login")

// portalAccounts is the store as the data portal's sign-in sees it (dataapi.AccountStore).
type portalAccounts struct{ s *Store }

var _ dataapi.AccountStore = portalAccounts{}

// PortalAccounts returns the data portal's in-process view of the store: EVE sign-in, server-side sessions (see
// PortalSessionTTL) and the account's keys under the owner limits.
//
// A session row names the EVE character and owner hash it was started with. Session maps it to an account through the
// stored identity on every call and reads only: a missing identity, or one whose owner hash changed, ends the session
// and creates nothing. Only SignIn resolves with a fresh sign-in, the one path that may create an account.
func PortalAccounts(s *Store) dataapi.AccountStore { return portalAccounts{s: s} }

// portalErr maps the store's refusals to the dataapi ones; anything else is returned as it is.
func portalErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrTooManyKeys):
		return dataapi.ErrAccountTooManyKeys
	case errors.Is(err, ErrCreateLimit):
		return dataapi.ErrAccountCreateLimit
	case errors.Is(err, ErrOwnerBlocked):
		return dataapi.ErrAccountBlocked
	case errors.Is(err, ErrBadName):
		return dataapi.ErrAccountKeyName
	case errors.Is(err, ErrNotFound):
		return dataapi.ErrAccountKeyNotFound
	case errors.Is(err, errDiscordLinked):
		return dataapi.ErrAccountDiscordLinked
	case errors.Is(err, ErrNoAccount), errors.Is(err, ErrIdentityChanged), errors.Is(err, ErrBadIdentity), errors.Is(err, ErrBadOwner):
		return dataapi.ErrAccountGone
	}
	return err
}

func (p portalAccounts) SignIn(ctx context.Context, characterID, ownerHash, name string) (string, dataapi.PortalSession, error) {
	s := p.s
	id := Identity{Provider: ProviderEVE, Subject: characterID, OwnerHash: ownerHash}
	if err := id.signedIn(); err != nil {
		return "", dataapi.PortalSession{}, portalErr(err)
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	prev, err := s.lookupIdentity(ctx, id)
	if err != nil {
		return "", dataapi.PortalSession{}, err
	}
	acct, err := s.resolveLocked(ctx, id, true)
	if err != nil {
		return "", dataapi.PortalSession{}, portalErr(err)
	}
	a, err := s.loadAccount(ctx, acct)
	if err != nil {
		return "", dataapi.PortalSession{}, portalErr(err)
	}
	// Under the lock: no deletion can run between the resolve and the new session.
	tok, row, err := s.insertSession(ctx, id, cleanOwnerName(name))
	if err != nil {
		return "", dataapi.PortalSession{}, err
	}
	out := portalSessionOf(a, row)
	out.Rehomed = prev.found && prev.ownerHash != ownerHash
	s.log.Info("portal_session_started")
	return tok, out, nil
}

func (p portalAccounts) Session(ctx context.Context, tok string) (dataapi.PortalSession, bool, error) {
	s := p.s
	row, ok, err := s.loadSession(ctx, tok)
	if err != nil || !ok {
		return dataapi.PortalSession{}, false, err
	}
	// Read-only: never Resolve here, which would create an account for an identity it does not find.
	idr, err := s.lookupIdentity(ctx, Identity{Provider: ProviderEVE, Subject: row.characterID})
	if err != nil {
		return dataapi.PortalSession{}, false, err
	}
	if !idr.found || idr.ownerHash != row.ownerHash {
		return dataapi.PortalSession{}, false, s.deleteSession(ctx, tok)
	}
	a, err := s.loadAccount(ctx, idr.account)
	if errors.Is(err, ErrNoAccount) {
		return dataapi.PortalSession{}, false, s.deleteSession(ctx, tok)
	}
	if err != nil {
		return dataapi.PortalSession{}, false, err
	}
	return portalSessionOf(a, row), true, nil
}

func portalSessionOf(a Account, row portalSession) dataapi.PortalSession {
	out := dataapi.PortalSession{
		Account: a.ID, CharacterID: row.characterID, OwnerHash: row.ownerHash, Name: row.name,
		CSRF: row.csrf, FormToken: row.formToken, CreatedAt: row.created, ExpiresAt: row.expires,
		Blocked: a.Blocked,
	}
	for _, li := range a.Identities {
		if li.Provider == ProviderDiscord {
			out.DiscordLinked = true
		}
	}
	return out
}

func (p portalAccounts) EndSession(ctx context.Context, tok string) error {
	return p.s.deleteSession(ctx, tok)
}

func (p portalAccounts) UseFormToken(ctx context.Context, tok, form string) (bool, error) {
	return p.s.useFormToken(ctx, tok, form)
}

func (p portalAccounts) Keys(ctx context.Context, acct string) ([]dataapi.PortalKey, error) {
	ks, err := p.s.List(ctx, acct)
	if err != nil {
		return nil, portalErr(err)
	}
	out := make([]dataapi.PortalKey, len(ks))
	for i, k := range ks {
		out[i] = portalKeyOf(k)
	}
	return out, nil
}

func portalKeyOf(k Key) dataapi.PortalKey {
	return dataapi.PortalKey{ID: k.ID, Name: k.Name, CreatedAt: k.CreatedAt, LastUsed: k.LastUsed, DailyCap: k.DailyCap, UsedToday: k.UsedToday}
}

func (p portalAccounts) CreateKey(ctx context.Context, acct, ownerName, name string) (dataapi.PortalKey, string, error) {
	if !accountRE.MatchString(acct) {
		return dataapi.PortalKey{}, "", dataapi.ErrAccountGone
	}
	k, secret, err := p.s.Create(ctx, acct, ownerName, name)
	if err != nil {
		return dataapi.PortalKey{}, "", portalErr(err)
	}
	return portalKeyOf(k), secret, nil
}

func (p portalAccounts) RevokeKey(ctx context.Context, acct, id string) error {
	if !accountRE.MatchString(acct) {
		return dataapi.ErrAccountGone
	}
	return portalErr(p.s.Revoke(ctx, acct, id))
}

func (p portalAccounts) DeleteAccount(ctx context.Context, characterID, ownerHash string) error {
	_, err := p.s.deleteAccountOf(ctx, Identity{Provider: ProviderEVE, Subject: characterID, OwnerHash: ownerHash}, true)
	return portalErr(err)
}

func (p portalAccounts) Limits() dataapi.PortalLimits {
	return dataapi.PortalLimits{MaxActive: p.s.opt.MaxPerOwner, MaxCreatesPerDay: MaxCreatesPerDay, DailyCap: p.s.opt.DailyCap, KeyNameMax: maxNameRunes}
}
