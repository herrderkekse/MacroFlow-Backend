// Share endpoints: an authenticated user POSTs a food/recipe/log payload and
// gets back a token + URL; anyone holding that URL can fetch the payload back
// or be redirected into the app, no account required. The server never
// interprets the payload beyond checking its size — the app owns that shape.
package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/herrderkekse/MacroFlow-Backend/internal/store"
)

// shareKinds are the payload shapes the app currently sends. Extend when the
// app adds another shareable thing.
var shareKinds = map[string]bool{
	"food":   true,
	"recipe": true,
	"log":    true,
}

const currentShareVersion = 1

// Rate-limit window for creating shares, keyed by the authenticated username
// rather than IP (the endpoint requires auth, so abuse is attributable).
const (
	shareCreateRateLimit  = 20
	shareCreateRateWindow = time.Hour
)

// Rate-limit window for the public read endpoints, keyed by IP. Generous
// relative to the contact form's, since legitimate use is one read per share.
const (
	shareReadRateLimit  = 60
	shareReadRateWindow = 5 * time.Minute
)

// shareCreateRequest is the POST /api/v1/share body.
type shareCreateRequest struct {
	Kind    string          `json:"kind"`
	Version int             `json:"version"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())

	if !s.shareCreateLimiter.allow(user, time.Now()) {
		writeError(w, http.StatusTooManyRequests, "too many shares created, please try again later")
		return
	}

	var req shareCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !shareKinds[req.Kind] {
		writeError(w, http.StatusBadRequest, "unknown share kind")
		return
	}
	if req.Version != currentShareVersion {
		writeError(w, http.StatusBadRequest, "unsupported payload version")
		return
	}
	if len(req.Payload) == 0 {
		writeError(w, http.StatusBadRequest, "missing payload")
		return
	}
	if int64(len(req.Payload)) > s.cfg.MaxShareBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "share payload too large")
		return
	}

	token, err := s.store.CreateShare(r.Context(), user, req.Kind, req.Version, req.Payload, time.Now(), s.cfg.ShareTTL)
	if err != nil {
		s.log.Error("creating share failed", "user", user, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token,
		"url":   s.shareURL(r, token),
	})
}

// getShareJSON serves the shared payload back to the app. Public: the
// recipient generally has no account on this server.
func (s *Server) getShareJSON(w http.ResponseWriter, r *http.Request) {
	if !s.shareReadLimiter.allow(clientIP(r), time.Now()) {
		writeError(w, http.StatusTooManyRequests, "too many requests, please try again later")
		return
	}

	token := r.PathValue("token")
	sh, err := s.store.GetShare(r.Context(), token, time.Now())
	if err == store.ErrShareNotFound {
		writeError(w, http.StatusNotFound, "share not found or expired")
		return
	}
	if err != nil {
		s.log.Error("fetching share failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kind":      sh.Kind,
		"version":   sh.Version,
		"payload":   sh.Payload,
		"createdAt": sh.CreatedAt,
	})
}

// getShareRedirect is the human-facing link (e.g. the one encoded in the QR):
// it just bounces a browser into the app via the custom URL scheme. Kept
// deliberately dumb — no rendered preview — so a plain http(s) redirect is
// all a browser has to handle, which is what keeps the QR scannable by stock
// camera apps that only auto-recognize http(s) URLs. The origin travels along
// as a query parameter because the custom-scheme link otherwise loses which
// server the token lives on — the recipient's app may not have (or may point
// elsewhere with) a sync account.
func (s *Server) getShareRedirect(w http.ResponseWriter, r *http.Request) {
	if !s.shareReadLimiter.allow(clientIP(r), time.Now()) {
		writeError(w, http.StatusTooManyRequests, "too many requests, please try again later")
		return
	}

	token := r.PathValue("token")
	if _, err := s.store.GetShare(r.Context(), token, time.Now()); err == store.ErrShareNotFound {
		writeError(w, http.StatusNotFound, "share not found or expired")
		return
	} else if err != nil {
		s.log.Error("fetching share failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	http.Redirect(w, r, "macroflow://share/"+token+"?origin="+url.QueryEscape(s.origin(r)), http.StatusFound)
}

// origin is the externally-reachable scheme://host shares live under,
// preferring the configured PublicOrigin and otherwise deriving it from the
// request itself.
func (s *Server) origin(r *http.Request) string {
	if s.cfg.PublicOrigin != "" {
		return s.cfg.PublicOrigin
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// shareURL builds the human-facing share link for token.
func (s *Server) shareURL(r *http.Request, token string) string {
	return s.origin(r) + "/s/" + token
}
