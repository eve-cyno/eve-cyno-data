package dataapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"eve-cyno.dev/go/data/catalog"
)

const (
	// HeaderAPIKey carries an API key (the alternative to "Authorization: Bearer <key>").
	HeaderAPIKey = "X-API-Key"
	// HeaderJaniceKey carries the caller's own Janice key to a byo-key tool.
	HeaderJaniceKey = catalog.HeaderJaniceKey

	// maxCredentialLen caps a presented API key or Janice key; a longer one is
	// refused rather than hashed or forwarded.
	maxCredentialLen = 256
)

// ErrInvalidAPIKey is what an Authenticator returns for a key that was presented but is
// not known (or is malformed). A request without any key is anonymous, not an error.
var ErrInvalidAPIKey = errors.New("invalid API key")

// Caller is who a request is from, as the tool endpoint and the MCP endpoint see it. The
// zero Caller is anonymous.
type Caller struct {
	// ID names the key (never the key itself); empty for an anonymous caller. It is the
	// rate-limit bucket of the caller (see CallerKeys) and what a log line may say.
	ID string
	// tiers are the tiers the key is allowed beyond the public ones.
	tiers []catalog.Tier
}

// NewCaller returns the authenticated caller id, allowed the given tiers (TierKeyed for
// an ordinary key).
func NewCaller(id string, tiers ...catalog.Tier) Caller {
	return Caller{ID: id, tiers: slices.Clone(tiers)}
}

// Authenticated reports whether the caller presented a valid key.
func (c Caller) Authenticated() bool { return c.ID != "" }

// Allows reports whether the caller may run a tool of tier t: public and byo-key tools
// are open to everyone (byo-key additionally needs the caller's own Janice key), keyed
// tools to a key whose allowance includes TierKeyed, disabled tools to nobody.
func (c Caller) Allows(t catalog.Tier) bool {
	switch t {
	case catalog.TierPublic, catalog.TierBYOKey:
		return true
	case catalog.TierKeyed:
		return c.Authenticated() && slices.Contains(c.tiers, catalog.TierKeyed)
	}
	return false
}

// Authenticator turns a request into a Caller. It returns the anonymous Caller and a nil
// error when the request carries no API key, ErrInvalidAPIKey when it carries one that is
// not accepted. Implementations must be safe for concurrent use and must never log or
// return the key. R3.5's key store plugs in here.
type Authenticator interface {
	Authenticate(r *http.Request) (Caller, error)
}

// APIKeyFrom extracts the API key a request presents: "Authorization: Bearer <key>"
// first, else the X-API-Key header. present is false when neither header is set; a
// header that is set but empty or too long yields present with an empty key, which no
// store accepts.
func APIKeyFrom(r *http.Request) (key string, present bool) {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if scheme, token, ok := strings.Cut(auth, " "); ok && strings.EqualFold(scheme, "Bearer") {
			return clampCredential(strings.TrimSpace(token)), true
		}
	}
	if v := r.Header.Get(HeaderAPIKey); v != "" {
		return clampCredential(strings.TrimSpace(v)), true
	}
	return "", false
}

func clampCredential(s string) string {
	if len(s) > maxCredentialLen {
		return ""
	}
	return s
}

// HashKey returns the lowercase hex SHA-256 of an API key: the only form of a key a key
// file (or any store) holds. Generate a key with `openssl rand -hex 32` and store
// `printf %s "$key" | sha256sum`.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// StaticKeys is an Authenticator over a fixed set of keys, held as SHA-256 hashes: for
// self-hosting and tests. Every key it knows may run the keyed tier.
type StaticKeys struct {
	byHash map[string]string // hash -> key id
}

// ParseKeyFile reads a key file: one `id:sha256hex` per line (id is 1-64 characters from
// [A-Za-z0-9._-], the hash 64 hex digits); blank lines and lines starting with # are
// skipped. Anything else is an error that names the line number but never echoes the line,
// so a plaintext key pasted by mistake does not end up in a log. A file without a key is
// an error too.
func ParseKeyFile(r io.Reader) (*StaticKeys, error) {
	k := &StaticKeys{byHash: map[string]string{}}
	ids := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 4096)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, hash, ok := strings.Cut(line, ":")
		hash = strings.ToLower(hash)
		if !ok || !validRequestID(id) || !validHash(hash) {
			return nil, fmt.Errorf("line %d: want id:sha256hex (id of 1-64 characters from A-Za-z0-9._-, then 64 hex digits; store only the hash of a key)", n)
		}
		if ids[id] {
			return nil, fmt.Errorf("line %d: duplicate key id", n)
		}
		if _, dup := k.byHash[hash]; dup {
			return nil, fmt.Errorf("line %d: duplicate key hash", n)
		}
		ids[id] = true
		k.byHash[hash] = id
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	if len(k.byHash) == 0 {
		return nil, errors.New("key file holds no keys")
	}
	return k, nil
}

// LoadKeyFile is ParseKeyFile over the file at path.
func LoadKeyFile(path string) (*StaticKeys, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open key file: %w", err)
	}
	defer f.Close()
	k, err := ParseKeyFile(f)
	if err != nil {
		return nil, fmt.Errorf("key file %s: %w", path, err)
	}
	return k, nil
}

func validHash(h string) bool {
	if len(h) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// Len is the number of keys.
func (k *StaticKeys) Len() int { return len(k.byHash) }

// Authenticate implements Authenticator.
func (k *StaticKeys) Authenticate(r *http.Request) (Caller, error) {
	key, present := APIKeyFrom(r)
	if !present {
		return Caller{}, nil
	}
	if id, ok := k.byHash[HashKey(key)]; ok && key != "" {
		return NewCaller(id, catalog.TierKeyed), nil
	}
	return Caller{}, ErrInvalidAPIKey
}

// authState is what the request middleware attaches to the context after authenticating.
type authState struct {
	caller Caller
	err    error
}

type authKey struct{}

// CallerFrom returns the Caller of the request the context belongs to: anonymous outside
// a request served by this package, when no Authenticator is configured, and when the
// presented key was invalid (see Authenticator).
func CallerFrom(ctx context.Context) Caller {
	st, _ := ctx.Value(authKey{}).(authState)
	return st.caller
}

// authErrorFrom returns the authentication error of the request (nil when there is none).
func authErrorFrom(ctx context.Context) error {
	st, _ := ctx.Value(authKey{}).(authState)
	return st.err
}

// CallerKeys names the rate-limit bucket by the authenticated key ("key:<id>"), and by
// client IP (ClientIP) for an anonymous caller or an invalid key, so guessing keys is
// limited per IP. It is the default KeyResolver.
type CallerKeys struct{}

// Key implements KeyResolver.
func (CallerKeys) Key(r *http.Request) string {
	if c := CallerFrom(r.Context()); c.Authenticated() {
		return "key:" + c.ID
	}
	return ClientIP(r)
}

// writeKeyedRefusal answers 401 api_key_required for an anonymous caller (403 key_not_permitted for
// a key without the keyed allowance).
func writeKeyedRefusal(w http.ResponseWriter, r *http.Request, caller Caller) {
	if caller.Authenticated() {
		writeError(w, r, http.StatusForbidden, "key_not_permitted", "this API key is not allowed to run keyed tools")
		return
	}
	writeError(w, r, http.StatusUnauthorized, "api_key_required", "this tool needs an API key: send it as 'Authorization: Bearer <key>' or the X-API-Key header")
}
