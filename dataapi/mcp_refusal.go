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

// rpcBusyCode is the JSON-RPC error code of a range ceiling refusal of a request that is not a
// tools/call: the first of the codes JSON-RPC 2.0 leaves to the server (-32000 to -32099).
const rpcBusyCode = -32000

// connectorBusy is the one text of a range ceiling refusal answered as JSON-RPC, with the wait in
// whole seconds. It points at no API key: a claude.ai or Claude Desktop connector cannot send one.
func connectorBusy(secs int) string {
	return fmt.Sprintf("This service is busy for Claude connectors right now; try again in %d seconds.", secs)
}

// Range ceiling labels of the access line (range_refused): the MCP route's shared ceiling and
// the live tools' shared ceiling.
const (
	refusedMCPCeiling  = "mcp_ceiling"
	refusedLiveCeiling = "live_ceiling"
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

// writeMCPCeilingRefusal answers a refusal of the range ceiling l to a POST of one JSON-RPC 2.0
// message to the MCP route in the protocol, so a connector reads it instead of a transport error:
// HTTP 200 with the request's id and the busy text (connectorBusy), as a result with isError for
// a tools/call and as the error rpcBusyCode for any other method; a notification gets 202 with
// no body, what the stateless transport answers every notification. Retry-After is kept on all
// of them. It records the ceiling's label for the access line (range_refused) and reports true.
// For anything else (another route or method, a batch, a body that is not one JSON-RPC 2.0
// request or notification, or one over the transport's limit) it writes nothing and reports
// false: the caller answers the HTTP 429.
func (a *API) writeMCPCeilingRefusal(w http.ResponseWriter, r *http.Request, retry time.Duration, l *rangeLimiter) bool {
	if r.Method != http.MethodPost || a.mcp == nil || r.URL.Path != a.prefix+"/mcp" {
		return false
	}
	id, method, ok := rpcMessage(r)
	if !ok {
		return false
	}
	label := refusedMCPCeiling
	if l.live {
		label = refusedLiveCeiling
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
		reply.Result = &rpcToolError{Content: []rpcText{{Type: "text", Text: connectorBusy(secs)}}, IsError: true}
	} else {
		reply.Error = &rpcError{Code: rpcBusyCode, Message: connectorBusy(secs)}
	}
	writeJSON(w, r, http.StatusOK, reply)
	return true
}

// rpcMessage decodes the body of a POST to the MCP transport as one JSON-RPC 2.0 request or
// notification, as the SDK decodes a message (the first JSON value; exact, case-sensitive keys;
// "jsonrpc" "2.0" and a string "method" required): its method and id, a string or an int64 (the
// SDK makes a numeric id an int64), nil for a notification (no id, or null). ok is false for
// anything else: a batch, a body that does not parse, a message without a method (a response), an
// id of another type, a body over the transport's limit or nested deeper than the SDK accepts. A
// peeked body is restored.
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

// markRangeRefused records on the request the range ceiling whose refusal was answered as
// JSON-RPC, for the access line.
func markRangeRefused(ctx context.Context, label string) {
	if m, ok := ctx.Value(metaKey{}).(meta); ok && m.rangeRefused != nil {
		*m.rangeRefused = label
	}
}
