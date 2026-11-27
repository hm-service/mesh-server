// Package httpx holds the small HTTP utilities shared by the API layer: JSON
// responses, request bodies, client IP resolution and concurrency gates.
package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// MaxBodyBytes bounds every JSON request body.
const MaxBodyBytes = 1 << 20

// WriteJSON writes v as the response body. The body is built by this package
// from typed values, so a failure can only come from a dead connection.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write response", "error", err)
	}
}

// WriteError writes the {"code": ...} body every non-2xx API answer uses.
func WriteError(w http.ResponseWriter, status int, code string) {
	WriteJSON(w, status, map[string]string{"code": code})
}

// WriteErrorDetail adds a human-readable message for endpoints whose input a
// person edits by hand.
func WriteErrorDetail(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]string{"code": code, "message": message})
}

// DecodeJSON reads one JSON value from the request body and rejects every field
// the destination does not declare. The rule lives here once instead of in a
// per-field check inside each handler: a body that cannot be decoded, or that
// carries a field the contract does not declare, is answered as 400
// invalid_request, and the handler returns immediately.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

// ReadBody buffers the raw body, which is what a device signs. A body over the
// limit fails with *http.MaxBytesError.
func ReadBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
}
