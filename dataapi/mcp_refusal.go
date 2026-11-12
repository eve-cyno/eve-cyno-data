package dataapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// rpcBusyCode is the JSON-RPC error code of a range caller's refusal of a request that is not a
// tools/call: the first of the codes JSON-RPC 2.0 leaves to the server (-32000 to -32099).
const rpcBusyCode = -32000

// connectorBusy is the text of a range caller's per-minute refusal answered in the protocol (the
// shared ceiling or the caller's own address limit, on the route or for the live tools), with the
// wait in whole seconds and the key hint of keyURL (connectorText).
func connectorBusy(secs int, keyURL string) string {
	return connectorText(fmt.Sprintf("This service is busy for Claude connectors right now; try again in %d seconds.", secs), keyURL)
}

// connectorDaily is the text of a range caller's daily cap refusal answered in the protocol: no
// wait in it (Retry-After carries the seconds until 00:00 UTC), and the key hint of keyURL.
func connectorDaily(keyURL string) string {
	return connectorText("Today's request limit for Claude connectors is used up; it resets at 00:00 UTC.", keyURL)
}

// connectorText ends the sentence of a range caller's refusal with the general key hint: a
// connector (claude.ai, Claude Desktop) cannot send a key, but another keyless client in the
// ranges can. Without a key page (keyURL "", no PublicURL) the sentence stands alone.
func connectorText(sentence, keyURL string) string {
	if keyURL == "" {
		return sentence
	}
	return sentence + " Clients that can send an Authorization header can use a free API key: " + keyURL
}

// accessKeysURL is the page that tells how to get a free API key, which the refusals point at:
// "" when the API has no PublicURL.
func accessKeysURL(publicURL string) string {
	if publicURL == "" {
		return ""
	}
	return publicURL + "/docs/access#access-keys"
}

// Labels of the access line (range_refused) for a range caller's refusal answered in the
// protocol: the MCP route's shared ceiling, the live tools' shared ceiling, the caller's own
// address limit (the route's or the live tools') and its daily cap.
const (
	refusedMCPCeiling  = "mcp_ceiling"
	refusedLiveCeiling = "live_ceiling"
	refusedAddress     = "address"
	refusedDailyCap    = "daily"
)

// rpcToolError is the result of a refused tools/call: isError with one text item, the shape
// mcpserver gives every tool-level error.
type rpcToolError struct {
	Content []rpcText `json:"content"`
	IsError bool      `json:"isError"`
}

type rpcText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcReply is a JSON-RPC 2.0 response: a result or an error.
type rpcReply struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  *rpcToolError `json:"result,omitempty"`
	Error   *rpcError     `json:"error,omitempty"`
}

// rangeCaller reports whether the caller of r is held to the range tier: no valid key and an
// address in the ranges of an API with the range tier (rangeFrom).
func rangeCaller(r *http.Request) bool {
	return rangeFrom(r.Context()) != "" && !CallerFrom(r.Context()).Authenticated()
}

// writeMCPCeilingRefusal answers a refusal of the range ceiling l in the protocol
// (writeConnectorRefusal), with the busy text and the ceiling's label.
func (a *API) writeMCPCeilingRefusal(w http.ResponseWriter, r *http.Request, retry time.Duration, l *rangeLimiter) bool {
	label := refusedMCPCeiling
	if l.live {
		label = refusedLiveCeiling
	}
	return a.writeConnectorRefusal(w, r, retry, label, func(secs int) string {
		return connectorBusy(secs, accessKeysURL(a.surface.PublicURL))
	})
}

// writeConnectorRefusal answers a range caller's refusal (rangeCaller) of a POST of one JSON-RPC
// 2.0 message to the MCP route in the protocol, so a connector reads it instead of a transport
// error: HTTP 200 with the request's id and text(wait in whole seconds), as a result with isError
// for a tools/call and as the error rpcBusyCode for any other method; a notification gets 202 with
// no body, what the stateless transport answers every notification. Retry-After is kept on all of
// them. It records label for the access line (range_refused) and reports true. For anything else
// (another caller, route or method, a batch, a body that is not one JSON-RPC 2.0 request or
// notification, one over the transport's limit, or one that must not be read: peekDeniedKey) it
// writes nothing and reports false: the caller answers the HTTP 429.
func (a *API) writeConnectorRefusal(w http.ResponseWriter, r *http.Request, retry time.Duration, label string, text func(secs int) string) bool {
	if r.Method != http.MethodPost || a.mcp == nil || r.URL.Path != a.prefix+"/mcp" || !rangeCaller(r) {
		return false
	}
	id, method, ok := rpcMessage(r)
	if !ok {
		return false
	}
	markRangeRefused(r.Context(), label)
	secs := retrySeconds(retry)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	if id == nil {
		w.WriteHeader(http.StatusAccepted)
		return true
	}
	reply := rpcReply{JSONRPC: "2.0", ID: id}
	if method == "tools/call" {
		reply.Result = &rpcToolError{Content: []rpcText{{Type: "text", Text: text(secs)}}, IsError: true}
	} else {
		reply.Error = &rpcError{Code: rpcBusyCode, Message: text(secs)}
	}
	writeJSON(w, r, http.StatusOK, reply)
	return true
}

// writeAddressRefusal answers the refusal of a range caller's own address limit on a route: in
// the protocol on the MCP route (writeConnectorRefusal, the busy text), else the plain 429.
func (a *API) writeAddressRefusal(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	if a.writeConnectorRefusal(w, r, retry, refusedAddress, func(secs int) string {
		return connectorBusy(secs, accessKeysURL(a.surface.PublicURL))
	}) {
		return
	}
	writeRateLimited(w, r, retry)
}

// writeAnonDailyRefusal answers the anonymous daily cap of the caller of r: a range caller's in
// the protocol on the MCP route (writeConnectorRefusal, the daily text), else the 429
// daily_quota_exceeded of writeAnonQuotaRefusal.
func (a *API) writeAnonDailyRefusal(w http.ResponseWriter, r *http.Request, retry time.Duration, cap int) {
	if a.writeConnectorRefusal(w, r, retry, refusedDailyCap, func(int) string {
		return connectorDaily(accessKeysURL(a.surface.PublicURL))
	}) {
		return
	}
	writeAnonQuotaRefusal(w, r, retry, cap, a.surface.PublicURL)
}

// rpcMessage decodes the body of a POST to the MCP transport as one JSON-RPC 2.0 request or
// notification, as the SDK decodes a message (the first JSON value; exact, case-sensitive keys;
// "jsonrpc" "2.0" and a string "method" required): its method and id, a string or an int64 (the
// SDK makes a numeric id an int64), nil for a notification (no id, or null). ok is false for
// anything else: a batch, a body that does not parse, a message without a method (a response), an
// id of another type, a body over the transport's limit or nested deeper than the SDK accepts, and
// a body that must not be read (peekDeniedKey: both pools of the route are full). A peeked body is
// restored.
func rpcMessage(r *http.Request) (id any, method string, ok bool) {
	body, err := peekBody(r, toolBodyMax+1)
	if err != nil || len(body) > toolBodyMax || jsonTooDeep(body, maxJSONDepth) {
		return nil, "", false
	}
	var env map[string]json.RawMessage
	if json.NewDecoder(bytes.NewReader(body)).Decode(&env) != nil {
		return nil, "", false
	}
	var version string
	if json.Unmarshal(env["jsonrpc"], &version) != nil || version != "2.0" {
		return nil, "", false
	}
	if json.Unmarshal(env["method"], &method) != nil {
		return nil, "", false
	}
	raw, has := env["id"]
	if !has {
		return nil, method, true
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, "", false
	}
	switch v := v.(type) {
	case nil:
		return nil, method, true
	case string:
		return v, method, true
	case float64:
		return int64(v), method, true
	}
	return nil, "", false
}

// markRangeRefused records on the request the limit whose refusal of a range caller was answered
// in the protocol, for the access line.
func markRangeRefused(ctx context.Context, label string) {
	if m, ok := ctx.Value(metaKey{}).(meta); ok && m.rangeRefused != nil {
		*m.rangeRefused = label
	}
}
