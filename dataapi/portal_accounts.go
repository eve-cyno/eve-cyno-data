package dataapi

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// AccountStore is how the data portal's sign-in reaches the key store's accounts, in
// process (keys.PortalAccounts implements it; dataapi cannot import keys). Sessions are
// kept server-side: the cookie holds a random token, the store only its SHA-256.
//
// A session names an EVE character and the owner hash it signed in with, never an account:
// every Session call maps it through the stored identity again, read-only, so a merged or
// re-homed account is followed at once, and a character that changed hands, was unlinked or
// deleted ends the session. Only SignIn may create an account (a fresh, verified sign-in).
type AccountStore interface {
	// SignIn resolves the account of a verified EVE sign-in (creating one for a character seen
	// for the first time, and moving a character whose owner hash changed to a new, empty
	// account) and starts a session. It returns the session token for the cookie.
	SignIn(ctx context.Context, characterID, ownerHash, name string) (token string, s PortalSession, err error)
	// Session looks up the session of token: ok is false when there is none, it expired, or its
	// identity is gone or changed hands (the session is then ended). It never creates anything.
	Session(ctx context.Context, token string) (s PortalSession, ok bool, err error)
	// EndSession deletes the session of token (none: no error).
	EndSession(ctx context.Context, token string) error
	// UseFormToken spends the one-time token of the session's key-creation form: true once for
	// the current value, which is then replaced, false for any other value.
	UseFormToken(ctx context.Context, token, formToken string) (bool, error)
	// Keys lists the active keys of the account, newest first, including keys under the legacy
	// owner ids of its identities.
	Keys(ctx context.Context, account string) ([]PortalKey, error)
	// CreateKey makes a key for the account under the owner limits and returns its secret.
	CreateKey(ctx context.Context, account, ownerName, name string) (PortalKey, string, error)
	// RevokeKey revokes one of the account's active keys.
	RevokeKey(ctx context.Context, account, id string) error
	// DeleteAccount deletes the account of the EVE sign-in with everything it holds, its
	// portal sessions included. An account with a Discord login is refused.
	DeleteAccount(ctx context.Context, characterID, ownerHash string) error
	// Limits are the owner limits the store applies.
	Limits() PortalLimits
}

// PortalSession is a signed-in portal visitor. Its LogValue hides every field.
type PortalSession struct {
	// Account is the account id the identity maps to now.
	Account string
	// CharacterID, OwnerHash and Name are the EVE sign-in the session was started with.
	CharacterID, OwnerHash, Name string
	// CSRF is the session's form token, sent back with every POST.
	CSRF string
	// FormToken is the current one-time token of the key-creation form.
	FormToken string
	// CreatedAt and ExpiresAt bound the session (an absolute lifetime, no sliding).
	CreatedAt, ExpiresAt time.Time
	// DiscordLinked is true when the account also has a Discord login.
	DiscordLinked bool
	// Blocked is true while the account is on the block list.
	Blocked bool
	// Rehomed is set by SignIn only: the character had another owner hash before, so it now has
	// a new, empty account and its earlier keys stay with the previous owner.
	Rehomed bool
}

// LogValue keeps the session out of log lines.
func (PortalSession) LogValue() slog.Value { return slog.StringValue("[portal session]") }

// PortalKey is one active key as the account page shows it.
type PortalKey struct {
	ID        string
	Name      string
	CreatedAt time.Time
	// LastUsed is the UTC date (YYYY-MM-DD) of the last charged request, "" when never used.
	LastUsed  string
	DailyCap  int
	UsedToday int
}

// PortalSessionTTL is the absolute lifetime of a data portal session from its sign-in; it never slides. The session
// cookie lives as long (Max-Age), and the store expires the row at the same time.
const PortalSessionTTL = 4 * time.Hour

// PortalLimits are the owner limits of the key store.
type PortalLimits struct {
	// MaxActive is the number of active keys an account may hold.
	MaxActive int
	// MaxCreatesPerDay is the number of keys an account may create per UTC day.
	MaxCreatesPerDay int
	// DailyCap is the daily request cap of a new key (0: none).
	DailyCap int
	// KeyNameMax is the longest key name, in characters.
	KeyNameMax int
}

// The refusals of an AccountStore.
var (
	ErrAccountTooManyKeys   = errors.New("dataapi: the account holds the most active keys allowed")
	ErrAccountCreateLimit   = errors.New("dataapi: the account created the most keys allowed today")
	ErrAccountBlocked       = errors.New("dataapi: the account is blocked")
	ErrAccountKeyName       = errors.New("dataapi: invalid key name")
	ErrAccountKeyNotFound   = errors.New("dataapi: no such active key in the account")
	ErrAccountGone          = errors.New("dataapi: the account or its sign-in identity is gone")
	ErrAccountDiscordLinked = errors.New("dataapi: the account has a Discord login")
)
