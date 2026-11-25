package api

import (
	"math/rand/v2"
	"net/http"
	"time"

	"mesh-server/core"
	"mesh-server/httpx"
)

// minAuthDelay is the floor of one login round trip. It is not configurable: a
// caller that could switch it off would lose the timing defence and the rate
// limit it provides, while tests only need to replace the wait itself.
const minAuthDelay = time.Second

// authJitter is the extra random delay added on top of the auth floor.
const authJitter = 200 * time.Millisecond

type authRequest struct {
	Token string `json:"token"`
}

// authResponse carries the credential issued by POST /api/auth. The credential
// is stateless: the server keeps no per-session state, so revoking one means
// rotating SESSION_SIGNING_KEY.
type authResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   int64  `json:"expires_at"`
}

// handleAuth exchanges AUTH_TOKEN for a bearer credential.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req authRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request")
		return
	}
	ok := req.Token != "" && s.tokenHash.Matches(req.Token)
	s.holdAuthFloor(start)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	nonce, err := core.RandomBytes(core.NonceBytes)
	if err != nil {
		s.logger.Error("generate nonce", "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal")
		return
	}
	expires := s.app.Now().Add(sessionTTL)
	httpx.WriteJSON(w, http.StatusOK, authResponse{
		AccessToken: core.SignBearer(s.sessionKey, expires, nonce),
		ExpiresAt:   expires.Unix(),
	})
}

// holdAuthFloor keeps a credential exchange from answering faster than the
// documented floor, so the response time does not tell which branch was taken.
func (s *Server) holdAuthFloor(start time.Time) {
	floor := minAuthDelay + rand.N(authJitter)
	if elapsed := time.Since(start); elapsed < floor {
		s.sleep(floor - elapsed)
	}
}
