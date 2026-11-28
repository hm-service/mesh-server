// Package httpx holds the small HTTP utilities shared by the API layer: JSON
// responses, request bodies, client IP resolution and concurrency gates.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// MaxBodyBytes bounds every JSON request body.
const MaxBodyBytes = 1 << 20

// WriteTimeout bounds one response transfer. It starts at the first byte the
// handler writes, not when the request arrived: a publish downloads up to 200
// MiB from the release URL and a credential issue waits on the seed's RPC, and
// each of those is bounded by its own back-end deadline. One deadline covering
// the whole business time would refuse them for reasons the client cannot see.
const WriteTimeout = 30 * time.Second

// WithWriteDeadline gives every response of h a transfer deadline that starts
// when the response does. It is the single entry point for both surfaces: a
// response that cannot be written out must not hold its concurrency slot until
// the client goes away.
func WithWriteDeadline(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&writeDeadlineWriter{ResponseWriter: w}, r)
	})
}

// writeDeadlineWriter sets the deadline on the first byte of its response.
type writeDeadlineWriter struct {
	http.ResponseWriter
	started bool
}

// Unwrap lets http.ResponseController reach the connection underneath.
func (w *writeDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *writeDeadlineWriter) WriteHeader(status int) {
	w.start()
	w.ResponseWriter.WriteHeader(status)
}

func (w *writeDeadlineWriter) Write(p []byte) (int, error) {
	w.start()
	return w.ResponseWriter.Write(p)
}

// start 只做一次：一个响应只有一个期限，从它真正开始传的那一刻算。
func (w *writeDeadlineWriter) start() {
	if w.started {
		return
	}
	w.started = true
	if err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		// 不实现期限的 ResponseWriter 只有测试替身与中间件包装；响应本身照旧写。
		slog.Debug("set response write deadline", "error", err)
	}
}

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

// DecodeJSON reads one complete JSON value from the request body and rejects
// every field the destination does not declare. The rule lives here once instead
// of in a per-field check inside each handler: a body that cannot be decoded,
// that carries a field the contract does not declare, or that holds anything
// after the value is answered as 400 invalid_request, and the handler returns
// immediately. Reading on past the value is also what makes MaxBodyBytes bound
// the whole request instead of its first value.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	// Token 跳过空白直到下一个记号：只剩空白时它给出 io.EOF。第二个值、尾随的
	// 字符与越过上限的读取都到不了 EOF，因此都算这份请求体不是完整的一份。
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
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
