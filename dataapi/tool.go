package dataapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"eve-cyno.dev/go/data/catalog"
	"eve-cyno.dev/go/data/tools"
)

// toolGate applies the tool tiers (core/catalog) of an internet-facing service to one
// call. It answers the refusal itself and returns ok=false, or returns the deps to run the
// tool with: a copy that carries the caller's own Janice key (the X-Janice-Key header, ""
// when absent), never the project's, so no public-surface call can spend the project key.
// An unknown name passes (the executor answers 404 unknown_tool).
//
//   - a key that was presented but is not accepted: 401 invalid_api_key, whatever the tool
//   - disabled: 403 tool_disabled (403, not 404: the tool exists and is refused on purpose,
//     and an agent should be told so rather than be sent looking for a typo)
//   - keyed, anonymous: 401 api_key_required; a key without the keyed allowance: 403
//     key_not_permitted
//   - byo-key without a Janice key: 400 janice_key_required
func (a *API) toolGate(w http.ResponseWriter, r *http.Request, name string) (*tools.Deps, bool) {
	caller := CallerFrom(r.Context())
	if authErrorFrom(r.Context()) != nil {
		writeError(w, r, http.StatusUnauthorized, "invalid_api_key", "the API key is not valid")
		return nil, false
	}
	janice := strings.TrimSpace(r.Header.Get(HeaderJaniceKey))
	if len(janice) > maxCredentialLen {
		writeError(w, r, http.StatusBadRequest, "invalid_request", HeaderJaniceKey+" is too long")
		return nil, false
	}
	if t, ok := catalog.Lookup(name); ok {
		switch {
		case t.Tier == catalog.TierDisabled:
			writeError(w, r, http.StatusForbidden, "tool_disabled", "tool "+name+" is disabled on the public API")
			return nil, false
		case t.Tier == catalog.TierKeyed && !caller.Allows(catalog.TierKeyed):
			writeKeyedRefusal(w, r, caller)
			return nil, false
		case t.Tier == catalog.TierBYOKey && janice == "":
			writeError(w, r, http.StatusBadRequest, "janice_key_required",
				"tool "+name+" runs with your own Janice API key: send it in the "+HeaderJaniceKey+" header")
			return nil, false
		}
	}
	return a.deps.Tools.WithJaniceKey(janice), true
}

// toolEnvelope is the 200 body in ToolFormatEnvelope.
type toolEnvelope struct {
	Tool        string           `json:"tool"`
	Version     string           `json:"version"`
	Result      toolEnvelopeBody `json:"result"`
	Attribution []tools.Source   `json:"attribution"`
}

type toolEnvelopeBody struct {
	// Text is exactly what the chat loop gets from tools.ExecuteTool.
	Text string `json:"text"`
	// Data is the tool's typed result, null for a tool without one.
	Data any `json:"data"`
}

// tool handles POST {prefix}/tool/{name}: runs a deterministic core tool
// in-process. The body is the tool's JSON arguments object. Registered only when
// Config.ToolAPI is on. The 200 body is the JSON envelope (the LLM-oriented text,
// the typed result where the tool has one, and the attribution of its upstreams) or,
// for a legacy mount (Config.ToolFormat), the bare text.
//
//   - 200 application/json  success, ToolFormatEnvelope
//   - 200 text/plain        success, ToolFormatText
//   - 400             body is not valid JSON
//   - 400             also: appraise_items without X-Janice-Key (janice_key_required)
//   - 401             keyed tool without a valid API key (api_key_required, invalid_api_key)
//   - 403             disabled tool (tool_disabled) on a ToolAPIPublic service
//   - 404             unknown tool name
//   - 413             body over 64 KiB
//   - 429             over the per-caller budget (ToolAPIPublic only)
//   - 503             tool layer unavailable (SDE not loaded)
//   - 504             the tool ran past Config.ToolTimeout
//   - 500             tool execution error
func (a *API) tool(w http.ResponseWriter, r *http.Request) {
	if a.deps.Tools == nil {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "deterministic layer unavailable (SDE not loaded)")
		return
	}

	name := r.PathValue("name")
	deps := a.deps.Tools
	if a.toolMode == ToolAPIPublic {
		var ok bool
		if deps, ok = a.toolGate(w, r, name); !ok {
			return
		}
	}

	body, ok := readBody(w, r, toolBodyMax)
	if !ok {
		return
	}
	var args map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &args); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
			return
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.toolTimeout)
	defer cancel()
	result, err := tools.ExecuteToolResult(ctx, deps, name, args)
	if err != nil {
		switch {
		case strings.HasPrefix(err.Error(), "unknown tool"):
			writeError(w, r, http.StatusNotFound, "unknown_tool", err.Error())
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, r, http.StatusGatewayTimeout, "timeout", "tool timed out")
		default:
			logFrom(r.Context()).Warn("tool_failed", "tool", name, "error", err)
			writeError(w, r, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}

	if a.toolFormat == ToolFormatText {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, result.Text)
		return
	}
	writeJSON(w, r, http.StatusOK, toolEnvelope{
		Tool:        result.Tool,
		Version:     result.Version,
		Result:      toolEnvelopeBody{Text: result.Text, Data: result.Data},
		Attribution: result.Attribution,
	})
}
