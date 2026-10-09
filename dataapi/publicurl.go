package dataapi

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ValidatePublicURL checks the public origin of the service (Config.PublicURL,
// DATAAPI_PUBLIC_URL) and returns it without a trailing slash: an absolute https URL
// with a host and nothing else (no path, query, fragment or credentials). The empty
// string is valid and means "relative links".
func ValidatePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("public URL %q: %w", raw, err)
	}
	switch {
	case u.Scheme != "https":
		return "", errors.New("public URL must use https")
	case u.Host == "" || u.Hostname() == "":
		return "", errors.New("public URL must have a host")
	case u.User != nil:
		return "", errors.New("public URL must not carry credentials")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", errors.New("public URL must not have a query or fragment")
	case u.Path != "" && u.Path != "/":
		return "", errors.New("public URL must be an origin without a path")
	}
	return u.Scheme + "://" + u.Host, nil
}

// absolutize turns the root-relative markdown links of a generated text ("](/v1/...)")
// into absolute ones under s.PublicURL. With no PublicURL it returns text unchanged.
func absolutize(s Surface, text string) string {
	if s.PublicURL == "" {
		return text
	}
	return strings.ReplaceAll(text, "](/", "]("+s.PublicURL+"/")
}
