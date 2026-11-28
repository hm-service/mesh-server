package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// decodeRequest runs DecodeJSON the way a handler does and reports what the
// handler decided and what the client would have received.
func decodeRequest(t *testing.T, body string) (bool, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/devices", strings.NewReader(body))
	decoded := DecodeJSON(recorder, request, &struct {
		Name string `json:"name"`
	}{})
	return decoded, recorder
}

// TestDecodeJSONAcceptsOneCompleteValue pins the accepted side: one JSON value
// is enough, and whitespace after it is still that same value.
func TestDecodeJSONAcceptsOneCompleteValue(t *testing.T) {
	for _, body := range []string{
		`{"name":"a"}`,
		`{"name":"a"} ` + "\n\t\r\n",
	} {
		decoded, recorder := decodeRequest(t, body)
		if !decoded {
			t.Fatalf("DecodeJSON(%q) rejected a complete request: %s", body, recorder.Body.String())
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("DecodeJSON(%q) wrote a response: %s", body, recorder.Body.String())
		}
	}
}

// TestDecodeJSONRejectsContentAfterTheValue pins the reported gap: a body that
// carries a second value or trailing bytes is not the single bounded JSON
// request the API contract describes, so it is refused like any other bad input.
func TestDecodeJSONRejectsContentAfterTheValue(t *testing.T) {
	for _, body := range []string{
		`{"name":"a"}{"name":"b"}`,
		`{"name":"a"}x`,
		`{"name":"a"}}`,
		`{"name":"a"} []`,
	} {
		decoded, recorder := decodeRequest(t, body)
		if decoded {
			t.Fatalf("DecodeJSON(%q) accepted a request with content after the value", body)
		}
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("DecodeJSON(%q) answered %d, want 400", body, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "invalid_request") {
			t.Fatalf("DecodeJSON(%q) body = %q, want invalid_request", body, recorder.Body.String())
		}
	}
}

// TestDecodeJSONBoundsTheWholeBody pins the size limit to the whole request:
// the value alone fits, and only reading on to the end of the body exceeds
// MaxBodyBytes, which must fail rather than leave the rest unexamined.
func TestDecodeJSONBoundsTheWholeBody(t *testing.T) {
	body := `{"name":"a"}` + strings.Repeat(" ", MaxBodyBytes)
	decoded, recorder := decodeRequest(t, body)
	if decoded {
		t.Fatal("DecodeJSON accepted a body longer than MaxBodyBytes")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("DecodeJSON answered %d, want 400", recorder.Code)
	}
}

// deadlineRecorder is a ResponseWriter that records the write deadline set
// through http.ResponseController, which is the interface net/http reaches for
// this on a real connection.
type deadlineRecorder struct {
	header   http.Header
	status   int
	body     []byte
	writeAt  time.Time
	setAt    time.Time
	deadline time.Time
}

func (r *deadlineRecorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *deadlineRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *deadlineRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.writeAt.IsZero() {
		r.writeAt = time.Now()
	}
	r.body = append(r.body, p...)
	return len(p), nil
}

func (r *deadlineRecorder) SetWriteDeadline(at time.Time) error {
	r.deadline = at
	r.setAt = time.Now()
	return nil
}

// TestResponseWriteDeadlineStartsWhenTheResponseDoes pins the bound F8 asks for:
// a response carries a write deadline, and that deadline starts at the first
// byte of the response rather than at the request, so a slow back end (a package
// download, a credential issue) is never charged against the transfer budget.
func TestResponseWriteDeadlineStartsWhenTheResponseDoes(t *testing.T) {
	const backend = 60 * time.Millisecond
	recorder := &deadlineRecorder{}
	var start time.Time
	handler := WithWriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start = time.Now()
		if !recorder.deadline.IsZero() {
			t.Error("a write deadline was set before the handler wrote anything")
		}
		time.Sleep(backend)
		WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/livez", nil))

	if recorder.deadline.IsZero() {
		t.Fatal("the response was written without a write deadline")
	}
	if !recorder.writeAt.After(start.Add(backend / 2)) {
		t.Fatalf("the body was written %v after the handler started, want the back end to run first", recorder.writeAt.Sub(start))
	}
	if recorder.setAt.Before(recorder.writeAt.Add(-time.Millisecond)) {
		t.Fatalf("the deadline was set %v before the first byte, want it at the write", recorder.writeAt.Sub(recorder.setAt))
	}
	if window := recorder.deadline.Sub(recorder.writeAt); window < WriteTimeout-100*time.Millisecond || window > WriteTimeout+100*time.Millisecond {
		t.Fatalf("the deadline is %v after the first byte, want %v", window, WriteTimeout)
	}
}

// TestResponseWithoutABodyGetsNoWriteDeadline pins the other half of the same
// rule: a request answered without a body leaves the connection alone.
func TestResponseWithoutABodyGetsNoWriteDeadline(t *testing.T) {
	recorder := &deadlineRecorder{}
	WithWriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 这个 handler 什么也不写，比如一个只做了副作用的请求。
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if !recorder.deadline.IsZero() {
		t.Fatalf("a response with no body set a write deadline of %v", recorder.deadline)
	}
}
