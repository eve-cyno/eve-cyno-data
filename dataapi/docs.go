package dataapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// renderedDocs are the generated descriptions of one API, rendered once in New.
type renderedDocs struct {
	yaml, json, llms, llmsFull []byte
}

// renderDocs renders every description of the Surface s. A failure (a Go type the schema
// deriver cannot describe, a route without a shared error response) is a startup error,
// never a half-served document.
func renderDocs(s Surface) (renderedDocs, error) {
	y, err := OpenAPIYAML(s)
	if err != nil {
		return renderedDocs{}, fmt.Errorf("openapi yaml: %w", err)
	}
	j, err := OpenAPIJSON(s)
	if err != nil {
		return renderedDocs{}, fmt.Errorf("openapi json: %w", err)
	}
	return renderedDocs{
		yaml:     y,
		json:     j,
		llms:     []byte(LLMsTxt(s)),
		llmsFull: []byte(LLMsFullTxt(s)),
	}, nil
}

func serveDoc(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveOpenAPIYAML handles GET {prefix}/openapi.yaml.
func (a *API) serveOpenAPIYAML(w http.ResponseWriter, _ *http.Request) {
	serveDoc(w, "application/yaml; charset=utf-8", a.docs.yaml)
}

// serveOpenAPIJSON handles GET {prefix}/openapi.json.
func (a *API) serveOpenAPIJSON(w http.ResponseWriter, _ *http.Request) {
	serveDoc(w, "application/json", a.docs.json)
}

// serveLLMs handles GET /llms.txt.
func (a *API) serveLLMs(w http.ResponseWriter, _ *http.Request) {
	serveDoc(w, "text/plain; charset=utf-8", a.docs.llms)
}

// serveLLMsFull handles GET /llms-full.txt.
func (a *API) serveLLMsFull(w http.ResponseWriter, _ *http.Request) {
	serveDoc(w, "text/plain; charset=utf-8", a.docs.llmsFull)
}

// mcpTransport handles every request to {prefix}/mcp: it is the MCP streamable-HTTP
// handler of Config.MCP, running inside this API's request ID, access log, panic
// recovery and (before it gets here) the Limits.MCP limiter.
func (a *API) mcpTransport(w http.ResponseWriter, r *http.Request) {
	if authErrorFrom(r.Context()) != nil {
		// A stale or mistyped key must not silently degrade to the anonymous tool list.
		writeError(w, r, http.StatusUnauthorized, "invalid_api_key", "the API key is not valid")
		return
	}
	if r.Method == http.MethodPost {
		// The MCP SDK accepts JSON-RPC batch arrays from clients that send no (or an old)
		// protocol version; one POST would then run many tool calls while the quota and the
		// rate limits count one request. Refuse batches before the handler sees them.
		body, ok := readBody(w, r, toolBodyMax)
		if !ok {
			return
		}
		if t := bytes.TrimLeft(body, " \t\r\n"); len(t) > 0 && t[0] == '[' {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "JSON-RPC batches are not accepted; send one message per request")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	a.mcp.ServeHTTP(w, r)
}
