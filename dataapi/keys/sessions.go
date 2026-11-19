package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// PortalSessionTTL is the absolute lifetime of a data portal session; it never slides.
const PortalSessionTTL = 4 * time.Hour

// sessionTokenRE is the shape of a session, CSRF or form token: 32 random bytes, base64url.
var sessionTokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// portalSession is one row of portal_sessions. The row holds the SHA-256 of the session token, the EVE sign-in it
// was started with (character id, owner hash, name), its two form tokens and its bounds; no address, no user agent.
type portalSession struct {
	characterID, ownerHash, name string
	csrf, formToken              string
	created, expires             time.Time
}

// newSessionToken draws 32 random bytes, base64url without padding (43 characters).
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("keys: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSessionToken is what the table stores for a session token.
func hashSessionToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// insertSession starts a session for a verified EVE sign-in and returns its token. Expired rows go first.
func (s *Store) insertSession(ctx context.Context, id Identity, name string) (string, portalSession, error) {
	if err := s.pruneSessions(ctx); err != nil {
		return "", portalSession{}, err
	}
	var toks [3]string
	for i := range toks {
		t, err := newSessionToken()
		if err != nil {
			return "", portalSession{}, err
		}
		toks[i] = t
	}
	now := s.now().UTC()
	row := portalSession{
		characterID: id.Subject, ownerHash: id.OwnerHash, name: name,
		csrf: toks[1], formToken: toks[2], created: now, expires: now.Add(PortalSessionTTL),
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO portal_sessions(token_hash, character_id, owner_hash, name, csrf, form_token, created_at, expires_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		hashSessionToken(toks[0]), row.characterID, row.ownerHash, row.name, row.csrf, row.formToken,
		row.created.Format(storedTime), row.expires.Format(storedTime)); err != nil {
		return "", portalSession{}, fmt.Errorf("keys: new session: %w", err)
	}
	return toks[0], row, nil
}

// loadSession returns the row of a live session. An expired row is deleted; a token of the wrong shape is no session.
func (s *Store) loadSession(ctx context.Context, tok string) (portalSession, bool, error) {
	if !sessionTokenRE.MatchString(tok) {
		return portalSession{}, false, nil
	}
	var row portalSession
	var created, expires string
	err := s.db.QueryRowContext(ctx,
		`SELECT character_id, owner_hash, name, csrf, form_token, created_at, expires_at FROM portal_sessions WHERE token_hash = ?`,
		hashSessionToken(tok)).Scan(&row.characterID, &row.ownerHash, &row.name, &row.csrf, &row.formToken, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return portalSession{}, false, nil
	}
	if err != nil {
		return portalSession{}, false, fmt.Errorf("keys: look up session: %w", err)
	}
	if row.created, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return portalSession{}, false, fmt.Errorf("keys: parse session created_at: %w", err)
	}
	if row.expires, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return portalSession{}, false, fmt.Errorf("keys: parse session expires_at: %w", err)
	}
	if !s.now().Before(row.expires) {
		return portalSession{}, false, s.deleteSession(ctx, tok)
	}
	return row, true, nil
}

// deleteSession ends the session of tok (none: nothing happens).
func (s *Store) deleteSession(ctx context.Context, tok string) error {
	if !sessionTokenRE.MatchString(tok) {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM portal_sessions WHERE token_hash = ?`, hashSessionToken(tok)); err != nil {
		return fmt.Errorf("keys: end session: %w", err)
	}
	return nil
}

// useFormToken replaces the form token of the live session of tok when form is its current value, in one statement,
// so two submissions of the same form cannot both pass.
func (s *Store) useFormToken(ctx context.Context, tok, form string) (bool, error) {
	if !sessionTokenRE.MatchString(tok) || !sessionTokenRE.MatchString(form) {
		return false, nil
	}
	next, err := newSessionToken()
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE portal_sessions SET form_token = ? WHERE token_hash = ? AND form_token = ? AND expires_at > ?`,
		next, hashSessionToken(tok), form, s.now().UTC().Format(storedTime))
	if err != nil {
		return false, fmt.Errorf("keys: use form token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("keys: use form token: %w", err)
	}
	return n == 1, nil
}

// pruneSessions deletes the expired sessions.
func (s *Store) pruneSessions(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM portal_sessions WHERE expires_at <= ?`,
		s.now().UTC().Format(storedTime)); err != nil {
		return fmt.Errorf("keys: prune sessions: %w", err)
	}
	return nil
}
