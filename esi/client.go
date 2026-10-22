// Package esi is the EVE Online ESI client shared by the data/tools platform (tools),
// the desktop app and the ingest pollers.
//
// It implements the ESI contract documented at
// https://developers.eveonline.com/docs/services/esi/: a descriptive User-Agent, a pinned X-Compatibility-Date on every
// request, an Expires-respecting ETag/If-None-Match cache, per-group token buckets driven
// by the X-Ratelimit-* headers plus the legacy error limit, Retry-After handling and
// X-Pages pagination. An optional persistent tier (Config.Store) sits behind the memory
// cache, and Forget drops entries by partition and scope. It uses net/http and golang.org/x/sync only and imports nothing
// in-repo (importcheck rule "esi-is-leaf").
//
// A Client is safe for concurrent use; build one per process with New and share it.
package esi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
)

// Request describes one ESI call.
type Request struct {
	// Method defaults to GET.
	Method string

	// Path is the route path starting with "/" and without a query string, for example
	// "/markets/10000002/orders" (the OpenAPI spec's paths, no trailing slash, no "/latest").
	Path string

	// Query holds the query parameters.
	Query url.Values

	// Body is a JSON request body (POST routes such as /universe/ids); sets Content-Type.
	Body []byte

	// Header carries extra request headers such as Accept-Language. The headers the client
	// owns (User-Agent, X-Compatibility-Date, Authorization, If-None-Match,
	// If-Modified-Since, Content-Type) are rejected.
	Header http.Header

	// AccessToken is an EVE SSO bearer token for authenticated routes.
	AccessToken string

	// Scope picks the rate-limit bucket and is part of the response-cache key (CachePartition
	// adds to it, never replaces it): ESI keeps one bucket per (group, application,
	// character). Authenticated callers pass a stable key such as "character:12345".
	// Without a Scope an authenticated request shares the bucket "auth:unscoped" and is
	// never cached, so one character's data cannot leak to another.
	Scope string

	// CachePartition, when set, partitions the response cache (and the in-flight coalescing)
	// alongside Scope: it never merges scopes, so one partition keeps characters apart, and
	// callers sharing one Scope never see each other's cached responses (for example one
	// partition per plugin). Scope alone keeps choosing the rate-limit bucket. An
	// authenticated request without a Scope is never cached, whatever its CachePartition.
	CachePartition string

	// UserAgentSuffix is appended to the client's User-Agent after one space (for example
	// "plugin/x@1.0.0"). Allowed: printable ASCII without '"' or '\\', at most 100 bytes;
	// anything else makes Do return an error before any HTTP call.
	UserAgentSuffix string

	// CompatibilityDate overrides the client's pinned X-Compatibility-Date for this request.
	CompatibilityDate string
}

// Response is an ESI answer. A non-2xx status is not a Go error (as with net/http); use
// Err for a typed one.
type Response struct {
	// Status is the HTTP status. A 304 revalidation is reported as the cached 200.
	Status int
	Header http.Header
	// Body is a private copy: callers may modify it.
	Body []byte

	// FromCache is true when the body came from the local cache, either because it had not
	// expired yet (no request sent) or because ESI answered 304 (Revalidated).
	FromCache   bool
	Revalidated bool

	ETag         string
	LastModified time.Time
	// Expires is when the data may change on ESI's side, on the client clock (adjusted for
	// skew with the Date header). Zero when the response carries none.
	Expires time.Time
	// Pages is X-Pages; 0 when absent.
	Pages int
	// Rate is the X-Ratelimit-* header family; zero Group when the route is not bucketed.
	Rate RateLimit
	// RetryAfter is, for a 420/429, how long ESI asked to wait.
	RetryAfter time.Duration

	method, path string
}

// Err returns nil for 2xx, *RateLimitError for 420/429, *StatusError otherwise.
func (r *Response) Err() error {
	return statusErr(r.method, r.path, r.Status, r.Header, r.Body, r.RetryAfter)
}

// DecodeJSON unmarshals the body into v.
func (r *Response) DecodeJSON(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("esi: decode %s %s: %w", r.method, r.path, err)
	}
	return nil
}

// Client is an ESI client. Create it with New.
type Client struct {
	baseURL     string
	userAgent   string
	compat      string
	hc          *http.Client
	sem         *semaphore.Weighted
	minInterval time.Duration
	maxWait     time.Duration
	maxRetries  int
	errFloor    int
	maxBody     int64
	cache       *cache
	store       Store
	lim         *limiter
	flights     singleflight.Group
	flightMu    sync.Mutex
	flightWait  map[string]*flight
	observer    func(CallEvent)

	// Clock and sleeper; tests replace them.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	c := &Client{
		now:   time.Now,
		sleep: sleepCtx,
		lim:   newLimiter(),
	}
	if cfg.Now != nil {
		c.now = cfg.Now
	}

	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	var err error
	if c.baseURL, err = normalizeBase(base); err != nil {
		return nil, err
	}

	c.compat = cfg.CompatibilityDate
	if c.compat == "" {
		c.compat = DefaultCompatibilityDate
	}
	if err := validateDate(c.compat, c.now()); err != nil {
		return nil, err
	}

	c.userAgent = UserAgent(cfg.Version, cfg.Contact)

	c.hc = cfg.HTTPClient
	if c.hc == nil {
		c.hc = &http.Client{Timeout: defaultHTTPTimeout}
	}

	maxConc := cfg.MaxConcurrent
	if maxConc <= 0 {
		maxConc = defaultMaxConcurrent
	}
	c.sem = semaphore.NewWeighted(int64(maxConc))

	c.minInterval = max(cfg.MinInterval, 0)
	switch {
	case cfg.MaxWait == 0:
		c.maxWait = defaultMaxWait
	case cfg.MaxWait < 0:
		c.maxWait = 0 // never wait: a needed wait is a *BackoffError
	default:
		c.maxWait = cfg.MaxWait
	}
	c.observer = cfg.Observer
	c.store = cfg.Store
	if cfg.GuardPercent > 0 {
		c.lim.floorPct = min(cfg.GuardPercent, 90)
	}
	switch {
	case cfg.MaxRetries == 0:
		c.maxRetries = defaultMaxRetries
	case cfg.MaxRetries < 0:
		c.maxRetries = 0
	default:
		c.maxRetries = cfg.MaxRetries
	}
	switch {
	case cfg.ErrorLimitFloor == 0:
		c.errFloor = defaultErrorFloor
	case cfg.ErrorLimitFloor < 0:
		c.errFloor = 0
	default:
		c.errFloor = cfg.ErrorLimitFloor
	}
	c.maxBody = cfg.MaxBodyBytes
	if c.maxBody <= 0 {
		c.maxBody = defaultMaxBody
	}
	switch {
	case cfg.CacheBytes == 0:
		c.cache = newCache(defaultCacheBytes)
	case cfg.CacheBytes > 0:
		c.cache = newCache(cfg.CacheBytes)
	}
	return c, nil
}

// UserAgent is the User-Agent this client sends.
func (c *Client) UserAgent() string { return c.userAgent }

// CompatibilityDate is the X-Compatibility-Date this client pins.
func (c *Client) CompatibilityDate() string { return c.compat }

// Get is Do for a plain GET.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, Request{Path: path, Query: query})
}

// GetJSON GETs path and decodes a 2xx JSON body into v; any other status is a typed error.
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, v any) error {
	r, err := c.Do(ctx, Request{Path: path, Query: query})
	if err != nil {
		return err
	}
	if err := r.Err(); err != nil {
		return err
	}
	return r.DecodeJSON(v)
}

// prepared is a validated Request.
type prepared struct {
	method, path, url string
	body              []byte
	header            http.Header
	token             string
	scope             string // rate-limit bucket scope; "" is the unauthenticated IP-level bucket
	partition         string // Request.CachePartition
	cacheScope        string // Request.Scope (the cache-key scope, "" when none)
	compat            string
	route             string
	key               string // cache key; "" when the request is not cacheable
	userAgent         string // full User-Agent for this request
}

// hasControlByte reports whether s holds a byte below 0x20 or 0x7f. The cache key joins its fields
// with a NUL byte, so a path holding one could forge the fields that follow it.
func hasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

var reservedHeaders = []string{"User-Agent", "X-Compatibility-Date", "Authorization", "If-None-Match", "If-Modified-Since", "Content-Type", "Host"}

func (c *Client) prepare(req Request) (*prepared, error) {
	p := &prepared{method: strings.ToUpper(req.Method), path: req.Path, body: req.Body, token: req.AccessToken, scope: req.Scope}
	if p.method == "" {
		p.method = http.MethodGet
	}
	if !strings.HasPrefix(req.Path, "/") || strings.ContainsAny(req.Path, "?#") || hasControlByte(req.Path) {
		return nil, fmt.Errorf("esi: invalid path %q (want \"/route\" without a query string or control characters)", req.Path)
	}
	if err := validateUserAgentSuffix(req.UserAgentSuffix); err != nil {
		return nil, err
	}
	p.userAgent = c.userAgent
	if req.UserAgentSuffix != "" {
		p.userAgent += " " + req.UserAgentSuffix
	}
	p.compat = req.CompatibilityDate
	if p.compat == "" {
		p.compat = c.compat
	} else if err := validateDate(p.compat, c.now()); err != nil {
		return nil, err
	}
	p.url = c.baseURL + req.Path
	if len(req.Query) > 0 {
		p.url += "?" + req.Query.Encode()
	}
	for k := range req.Header {
		for _, r := range reservedHeaders {
			if strings.EqualFold(k, r) {
				return nil, fmt.Errorf("esi: header %s is managed by the client", k)
			}
		}
	}
	p.header = req.Header
	if p.token != "" && p.scope == "" {
		p.scope = unscopedAuth
	}
	p.partition = req.CachePartition
	p.cacheScope = req.Scope
	p.route = routeKey(p.method, req.Path)

	cacheable := p.method == http.MethodGet && c.cache != nil && (req.AccessToken == "" || req.Scope != "")
	if cacheable {
		p.key = cacheKey(p.method, p.url, req.Scope, req.CachePartition, p.compat, p.header.Get("Accept-Language")+"|"+p.header.Get("X-Tenant"))
	}
	return p, nil
}

// maxUserAgentSuffix is the longest Request.UserAgentSuffix, in bytes.
const maxUserAgentSuffix = 100

// validateUserAgentSuffix accepts printable ASCII without '"' or '\\' up to maxUserAgentSuffix bytes.
func validateUserAgentSuffix(s string) error {
	if len(s) > maxUserAgentSuffix {
		return fmt.Errorf("esi: User-Agent suffix longer than %d bytes", maxUserAgentSuffix)
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b > 0x7e || b == '"' || b == '\\' {
			return fmt.Errorf("esi: User-Agent suffix has a character that is not allowed (printable ASCII without quote or backslash)")
		}
	}
	return nil
}

// Do sends req and returns ESI's answer.
//
// A fresh cached GET is returned without a request. An expired one is revalidated with
// If-None-Match and a 304 yields the cached body. Before sending, Do waits for the
// concurrency limit, MinInterval, the legacy error-limit pause and the (group, scope)
// token bucket; needing to wait longer than Config.MaxWait returns *BackoffError without
// sending anything. A 420/429 is retried after its Retry-After (up to MaxRetries, each
// wait within MaxWait) and otherwise returned as the Response.
//
// The error is non-nil only when no usable response exists: transport failure, a
// back-off (*BackoffError), an oversized body or an invalid request. HTTP error statuses
// are in Response.Status; Response.Err types them.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	p, err := c.prepare(req)
	if err != nil {
		return nil, err
	}
	if p.key == "" {
		return c.fetch(ctx, p, nil)
	}
	ent := c.cache.get(p.key)
	if ent == nil {
		ent = c.loadPersisted(ctx, p)
	}
	if ent != nil && c.now().Before(ent.expires) {
		c.observe(CallEvent{Route: p.route, Cache: CacheHit, RateRemaining: -1, RateLimit: -1, ErrorLimitRemain: -1})
		return ent.response(p, true, false), nil
	}

	// Callers asking for the same expired or missing entry share one upstream request (a
	// burst on a cold key costs ESI one call, not one per caller). The shared request is
	// not tied to any single caller's context, so one caller leaving does not cut it for
	// the others; it is cancelled when its last caller has left, and in any case bounded
	// by flightTimeout. Each caller leaves when its own context ends. Store.Save runs
	// after the request without cancellation or that deadline, so a Store bounds its own
	// I/O time.
	fl := c.joinFlight(ctx, p.key)
	defer c.leaveFlight(p.key, fl)
	ch := c.flights.DoChan(p.key, func() (any, error) {
		return c.fetch(fl.ctx, p, ent)
	})
	select {
	case <-ctx.Done():
		return nil, &BackoffError{Until: c.now(), Reason: "context ended", Err: ctx.Err()}
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*Response).clone(), nil
	}
}

// flight is the context of one shared upstream request and the number of callers still
// waiting for it.
type flight struct {
	ctx     context.Context
	cancel  context.CancelFunc
	waiters int
}

// joinFlight registers a caller for key and returns the flight it waits on, creating it
// (detached from ctx's cancellation, bounded by flightTimeout) for the first caller.
func (c *Client) joinFlight(ctx context.Context, key string) *flight {
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	fl := c.flightWait[key]
	if fl == nil {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flightTimeout)
		fl = &flight{ctx: fctx, cancel: cancel}
		if c.flightWait == nil {
			c.flightWait = make(map[string]*flight)
		}
		c.flightWait[key] = fl
	}
	fl.waiters++
	return fl
}

// leaveFlight unregisters a caller; the last one to leave cancels the shared request
// (a no-op when it already finished).
func (c *Client) leaveFlight(key string, fl *flight) {
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	fl.waiters--
	if fl.waiters > 0 {
		return
	}
	fl.cancel()
	if c.flightWait[key] == fl {
		delete(c.flightWait, key)
	}
}

// flightTimeout bounds one shared upstream request (waits for the rate limit included).
const flightTimeout = 2 * time.Minute

// clone returns a Response whose Header and Body the caller may modify.
func (r *Response) clone() *Response {
	cp := *r
	cp.Header = cloneHeader(r.Header)
	cp.Body = cloneBytes(r.Body)
	return &cp
}

// fetch sends the request (after admission) and handles 304, 420/429 retries and caching.
// ent is the expired cache entry to revalidate, nil when there is none.
func (c *Client) fetch(ctx context.Context, p *prepared, ent *entry) (*Response, error) {
	var last *Response
	for attempt := 0; ; attempt++ {
		tk, err := c.admit(ctx, p)
		if err != nil {
			if last != nil && errors.Is(err, ErrBackedOff) {
				return last, nil // retrying a 429 would have to wait too long: hand back the 429
			}
			var be *BackoffError
			if errors.As(err, &be) && be.Err == nil {
				c.observe(CallEvent{Route: p.route, Cache: cacheLabel(p, ent), BackedOff: true, Reason: be.Reason, RateRemaining: -1, RateLimit: -1, ErrorLimitRemain: -1})
			}
			return nil, err
		}
		started := c.now()
		r, err := c.exchange(ctx, p, ent, tk)
		if err != nil {
			return nil, err
		}
		now := c.now()
		took := now.Sub(started)

		switch {
		case r.Status == http.StatusNotModified:
			if ent == nil {
				return nil, fmt.Errorf("%s %s: %w", p.method, p.path, ErrNotModifiedWithoutCache)
			}
			c.observe(sentEvent(p, ent, r, took))
			ne := ent.revalidated(r.Header, now)
			if c.cache.put(ne) {
				c.persist(context.WithoutCancel(ctx), ne)
			}
			return ne.response(p, true, true), nil

		case r.Status == 420 || r.Status == http.StatusTooManyRequests:
			c.onRateLimited(p, r, now)
			c.observe(sentEvent(p, ent, r, took))
			last = r
			if attempt < c.maxRetries && r.RetryAfter <= c.maxWait {
				continue
			}
			return r, nil

		default:
			c.observe(sentEvent(p, ent, r, took))
			if storable(p.method, r.Status, r.Header, r.Expires, now) && p.key != "" {
				ne := &entry{
					key: p.key, status: r.Status, header: cloneHeader(r.Header), body: cloneBytes(r.Body),
					etag: r.ETag, lastModified: r.LastModified, expires: r.Expires,
					partition: p.partition, scope: p.cacheScope, storedAt: now,
				}
				if c.cache.put(ne) {
					c.persist(context.WithoutCancel(ctx), ne)
				}
			}
			return r, nil
		}
	}
}

// cacheLabel is the CallEvent.Cache of a request that has not been answered yet.
func cacheLabel(p *prepared, ent *entry) string {
	switch {
	case p.key == "":
		return CacheNone
	case ent != nil:
		return CacheStale
	default:
		return CacheMiss
	}
}

// onRateLimited registers a 420/429 so later requests wait instead of retrying blindly,
// and records how long ESI asked to wait in r.RetryAfter.
func (c *Client) onRateLimited(p *prepared, r *Response, now time.Time) {
	wait, ok := parseRetryAfter(r.Header, now)
	if !ok || wait <= 0 {
		wait = defaultRetryAfter
		if r.Status == 420 {
			if v, err := strconv.Atoi(strings.TrimSpace(r.Header.Get("X-ESI-Error-Limit-Reset"))); err == nil && v > 0 {
				wait = time.Duration(v) * time.Second
			}
		}
	}
	r.RetryAfter = wait
	if r.Status == 420 {
		return // observeErrorLimit already paused every route
	}
	if r.Rate.Group != "" {
		if b := c.lim.bucketForGroup(p.route, r.Rate.Group, p.scope); b != nil {
			b.block(now, wait)
		}
		return
	}
	c.lim.blockRoute(p.route, p.scope, now.Add(wait))
}

// ticket is the admission of one request: its token reservation (nil when the route's
// group is not known yet) and, implicitly, one concurrency slot released by exchange.
type ticket struct {
	b *bucket
}

// admit blocks until the request may be sent: error-limit pause, Retry-After block, token
// bucket, MinInterval spacing, concurrency slot. It returns *BackoffError when a wait would
// exceed MaxWait or the context ends first.
func (c *Client) admit(ctx context.Context, p *prepared) (*ticket, error) {
	deadline := c.now().Add(c.maxWait)
	tk := &ticket{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, &BackoffError{Until: c.now(), Reason: "context ended", Err: err}
		}
		now := c.now()
		if d, why := c.lim.pauseFor(now, p.route, p.scope); d > 0 {
			if err := c.waitFor(ctx, now, d, deadline, why); err != nil {
				return nil, err
			}
			continue
		}
		b := c.lim.bucketFor(p.route, p.scope)
		if b == nil {
			break
		}
		ok, d, certain := b.reserve(now)
		if ok {
			tk.b = b
			break
		}
		why := "rate-limit bucket empty"
		if !certain {
			why = "rate-limit bucket: waiting for in-flight responses"
		}
		if err := c.waitFor(ctx, now, d, deadline, why); err != nil {
			return nil, err
		}
	}

	undo := func() {
		if tk.b != nil {
			tk.b.cancel()
		}
	}
	if d := c.lim.slot(c.now(), c.minInterval); d > 0 {
		if err := c.sleep(ctx, d); err != nil {
			undo()
			return nil, &BackoffError{Until: c.now().Add(d), Reason: "spacing requests", Err: err}
		}
	}
	if err := c.sem.Acquire(ctx, 1); err != nil {
		undo()
		return nil, &BackoffError{Until: c.now(), Reason: "waiting for a request slot", Err: err}
	}
	return tk, nil
}

// waitFor sleeps (at most waitStep, so the caller re-evaluates against fresh headers) or
// fails with *BackoffError when the wait ends after deadline.
func (c *Client) waitFor(ctx context.Context, now time.Time, d time.Duration, deadline time.Time, why string) error {
	until := now.Add(d)
	if until.After(deadline) {
		return &BackoffError{Until: until, Reason: why}
	}
	if err := c.sleep(ctx, min(d, waitStep)); err != nil {
		return &BackoffError{Until: until, Reason: why, Err: err}
	}
	return nil
}

// exchange performs the HTTP round trip while holding the concurrency slot, settles the
// token reservation from the response headers and builds the Response (cache hits and
// 304 handling stay in Do).
func (c *Client) exchange(ctx context.Context, p *prepared, ent *entry, tk *ticket) (*Response, error) {
	defer c.sem.Release(1)
	cancel := func() {
		if tk.b != nil {
			tk.b.cancel()
		}
	}

	var body io.Reader
	if p.body != nil {
		body = bytes.NewReader(p.body)
	}
	hreq, err := http.NewRequestWithContext(ctx, p.method, p.url, body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("esi: build request %s %s: %w", p.method, p.path, err)
	}
	for k, vs := range p.header {
		for _, v := range vs {
			hreq.Header.Add(k, v)
		}
	}
	hreq.Header.Set("User-Agent", p.userAgent)
	hreq.Header.Set("X-Compatibility-Date", p.compat)
	if hreq.Header.Get("Accept") == "" {
		hreq.Header.Set("Accept", "application/json")
	}
	if p.body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		hreq.Header.Set("Authorization", "Bearer "+p.token)
	}
	if ent != nil && ent.etag != "" {
		hreq.Header.Set("If-None-Match", ent.etag)
	}

	resp, err := c.hc.Do(hreq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("esi: %s %s: %w", p.method, p.path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("esi: read %s %s: %w", p.method, p.path, err)
	}
	if int64(len(raw)) > c.maxBody {
		cancel()
		return nil, fmt.Errorf("esi: %s %s: %w (limit %d bytes)", p.method, p.path, ErrBodyTooLarge, c.maxBody)
	}

	now := c.now()
	rl := parseRateLimit(resp.Header)
	c.settle(tk, p, rl, now)
	c.lim.observeErrorLimit(now, resp.Header, resp.StatusCode, c.errFloor)

	pages, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("X-Pages")))
	return &Response{
		Status:       resp.StatusCode,
		Header:       resp.Header,
		Body:         raw,
		ETag:         resp.Header.Get("ETag"),
		LastModified: parseTime(resp.Header.Get("Last-Modified")),
		Expires:      localExpiry(resp.Header, now),
		Pages:        max(pages, 0),
		Rate:         rl,
		method:       p.method,
		path:         p.path,
	}, nil
}

// settle books a response against its group's bucket (learning the route's group on first
// sight) and releases the reservation held on a different bucket, if any.
func (c *Client) settle(tk *ticket, p *prepared, rl RateLimit, now time.Time) {
	var nb *bucket
	if rl.Group != "" {
		nb = c.lim.bucketForGroup(p.route, rl.Group, p.scope)
	}
	if tk.b != nil && tk.b != nb {
		tk.b.settle(now, true, RateLimit{Remaining: -1, Used: -1})
	}
	if nb != nil {
		nb.settle(now, tk.b == nb, rl)
	}
}

// response renders a cached entry as a Response (the body and headers are copies).
func (e *entry) response(p *prepared, fromCache, revalidated bool) *Response {
	pages, _ := strconv.Atoi(strings.TrimSpace(e.header.Get("X-Pages")))
	return &Response{
		Status:       e.status,
		Header:       cloneHeader(e.header),
		Body:         cloneBytes(e.body),
		FromCache:    fromCache,
		Revalidated:  revalidated,
		ETag:         e.etag,
		LastModified: e.lastModified,
		Expires:      e.expires,
		Pages:        max(pages, 0),
		method:       p.method,
		path:         p.path,
	}
}

// revalidated returns a copy of e refreshed with the headers of a 304 answer.
func (e *entry) revalidated(h http.Header, now time.Time) *entry {
	ne := *e
	ne.elem = nil
	ne.storedAt = now
	ne.header = cloneHeader(e.header)
	for _, k := range []string{"Expires", "Last-Modified", "Date", "Cache-Control", "ETag"} {
		if v := h.Get(k); v != "" {
			ne.header.Set(k, v)
		}
	}
	if v := h.Get("ETag"); v != "" {
		ne.etag = v
	}
	if t := parseTime(h.Get("Last-Modified")); !t.IsZero() {
		ne.lastModified = t
	}
	ne.expires = localExpiry(ne.header, now)
	return &ne
}

// sleepCtx sleeps for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
