package diff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

type entry struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Cassette is an http.RoundTripper that replays recorded responses.
type Cassette struct{ m map[string]entry }

func LoadCassette(path string) (*Cassette, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]entry
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &Cassette{m: m}, nil
}

// Sig builds the cassette lookup key. The key format is frozen: the checked-in
// cassettes in core/testdata/cassettes were recorded with it (by the since
// deleted Python capture_golden.py recorder), so changing it requires
// re-keying or re-recording them.
func Sig(method string, u *url.URL, body []byte) string {
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0)
	for _, k := range keys {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			if v == "" {
				continue // mirrors Python parse_qsl default: drops empty values
			}
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	path := u.Path
	if len(parts) > 0 {
		path += "?" + strings.Join(parts, "&")
	}
	h := sha256.Sum256(body)
	return fmt.Sprintf("%s %s|%s", strings.ToLower(method), path, hex.EncodeToString(h[:])[:16])
}

func (c *Cassette) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	key := Sig(req.Method, req.URL, body)
	e, ok := c.m[key]
	if !ok {
		return nil, fmt.Errorf("cassette miss: %s", key)
	}
	resp := &http.Response{
		StatusCode: e.Status,
		Body:       io.NopCloser(bytes.NewBufferString(e.Body)),
		Header:     make(http.Header),
	}
	for k, v := range e.Headers {
		resp.Header.Set(k, v)
	}
	return resp, nil
}
