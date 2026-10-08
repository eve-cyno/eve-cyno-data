package dataapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// errorBody is the JSON shape of every non-2xx response.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError answers with the JSON error shape. code is one of the stable codes
// listed in the package doc.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, r, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// writeJSON encodes v before touching the response, so an encoding failure is a
// clean 500 instead of a truncated 200.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		logFrom(r.Context()).Error("encode_response_failed", "error", err)
		buf.Reset()
		status = http.StatusInternalServerError
		_ = json.NewEncoder(&buf).Encode(errorBody{Error: errorDetail{Code: "internal_error", Message: "could not encode the response"}})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// decodeJSON reads one JSON value of at most max bytes from the body into dst.
// On failure it has already written the error response (413 for an oversize
// body, 400 otherwise) and returns false. Unknown fields are accepted.
func decodeJSON(w http.ResponseWriter, r *http.Request, max int64, dst any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(dst)
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		return false
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
	return false
}

// readBody reads the whole body, at most max bytes. On failure it has already
// written the error response and returns false.
func readBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err == nil {
		return body, true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		return nil, false
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", "could not read the request body")
	return nil, false
}
