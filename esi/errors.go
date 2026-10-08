package esi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Sentinel errors. Match them with errors.Is; the concrete types below carry detail
// (errors.As).
var (
	// ErrBackedOff means no request was sent: a rate limit, the legacy error limit or a
	// Retry-After back-off is active and waiting it out would exceed Config.MaxWait (or
	// the context ended while waiting).
	ErrBackedOff = errors.New("esi: backed off, request not sent")

	// ErrRateLimited is the sentinel of *RateLimitError (HTTP 420 or 429).
	ErrRateLimited = errors.New("esi: rate limited")

	// ErrNotModifiedWithoutCache means ESI answered 304 to a request that carried no
	// If-None-Match from this client, so there is no body to return. It should not happen.
	ErrNotModifiedWithoutCache = errors.New("esi: 304 Not Modified but nothing is cached")

	// ErrBodyTooLarge means a response body exceeded Config.MaxBodyBytes.
	ErrBodyTooLarge = errors.New("esi: response body too large")

	// ErrTooManyPages means X-Pages exceeded PageOptions.MaxPages.
	ErrTooManyPages = errors.New("esi: too many pages")

	// ErrInconsistentPages is the sentinel of *InconsistentPagesError.
	ErrInconsistentPages = errors.New("esi: pages carry different Last-Modified values")
)

// BackoffError is returned when a request was NOT sent because the client is backing
// off (see ErrBackedOff). Until is when sending could resume; Err is set when the
// context ended first.
type BackoffError struct {
	Until  time.Time
	Reason string
	Err    error
}

func (e *BackoffError) Error() string {
	s := fmt.Sprintf("esi: request not sent: %s (until %s)", e.Reason, e.Until.UTC().Format(time.RFC3339))
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes ErrBackedOff and, when set, the context error.
func (e *BackoffError) Unwrap() []error {
	if e.Err != nil {
		return []error{ErrBackedOff, e.Err}
	}
	return []error{ErrBackedOff}
}

// StatusError is a non-2xx ESI response other than 420/429 (see RateLimitError).
type StatusError struct {
	Method string
	Path   string
	Status int
	Body   string // truncated to a short snippet
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("esi: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// RateLimitError is an HTTP 420 (legacy error limit) or 429 (bucket limit) response
// that survived the client's own retries.
type RateLimitError struct {
	Method     string
	Path       string
	Status     int
	RetryAfter time.Duration // how long ESI asked us to wait; zero when unknown
	Group      string        // X-Ratelimit-Group, empty for the legacy error limit
}

func (e *RateLimitError) Error() string {
	g := e.Group
	if g == "" {
		g = "error-limit"
	}
	return fmt.Sprintf("esi: %s %s: HTTP %d rate limited (%s), retry after %s", e.Method, e.Path, e.Status, g, e.RetryAfter)
}

// Unwrap returns ErrRateLimited.
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// InconsistentPagesError reports that the pages of one paginated resource carry
// different Last-Modified values, i.e. the data changed between page requests
// (https://developers.eveonline.com/docs/services/esi/best-practices/, "Caching").
// The pages are still returned next to it; they may contain duplicates or gaps.
type InconsistentPagesError struct {
	Path string
}

func (e *InconsistentPagesError) Error() string {
	return fmt.Sprintf("esi: GET %s: pages carry different Last-Modified values (data changed mid-fetch)", e.Path)
}

// Unwrap returns ErrInconsistentPages.
func (e *InconsistentPagesError) Unwrap() error { return ErrInconsistentPages }

// snippet returns a short single-line excerpt of an error body.
func snippet(body []byte) string {
	const max = 160
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

// statusErr builds the typed error for a non-2xx response (nil for 2xx).
func statusErr(method, path string, status int, h http.Header, body []byte, retryAfter time.Duration) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == 420 || status == http.StatusTooManyRequests:
		return &RateLimitError{Method: method, Path: path, Status: status, RetryAfter: retryAfter, Group: h.Get("X-Ratelimit-Group")}
	default:
		return &StatusError{Method: method, Path: path, Status: status, Body: snippet(body)}
	}
}
