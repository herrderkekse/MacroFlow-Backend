// Package api exposes the sync change-log over HTTP. The endpoint contract is
// specified in SYNC.md — keep the two in lockstep.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/herrderkekse/MacroFlow-Backend/internal/config"
	"github.com/herrderkekse/MacroFlow-Backend/internal/store"
)

// Server wires the store and config into an http.Handler.
type Server struct {
	cfg   *config.Config
	store *store.Store
	log   *slog.Logger
}

// New returns a Server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, log: log}
}

// Handler builds the routed, middleware-wrapped http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Health check without auth, useful for container/orchestrator probes.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Account endpoints are unauthenticated (the caller has no account yet) but
	// still body-size capped.
	mux.Handle("POST /api/v1/auth/register", s.limitBody(http.HandlerFunc(s.register)))
	mux.Handle("POST /api/v1/auth/login", s.limitBody(http.HandlerFunc(s.login)))

	mux.Handle("GET /api/v1/sync/ping", s.auth(http.HandlerFunc(s.ping)))
	mux.Handle("GET /api/v1/sync/usage", s.auth(http.HandlerFunc(s.usage)))
	mux.Handle("GET /api/v1/sync/changes", s.auth(http.HandlerFunc(s.pull)))
	mux.Handle("POST /api/v1/sync/changes", s.auth(http.HandlerFunc(s.push)))

	return mux
}

// limitBody caps the request body at cfg.MaxBodyBytes without requiring auth.
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// userKey is the context key under which the authenticated username is stored.
type userKey struct{}

// auth enforces HTTP Basic auth and stashes the username in the request
// context. It also caps the request body at cfg.MaxBodyBytes.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)

		username, password, ok := r.BasicAuth()
		// authenticate resolves the canonical user id (a DB account is stored
		// lower-cased) so the same credentials key the same change log whatever
		// casing the client sent.
		user, authed := s.authenticate(r.Context(), username, password)
		if !ok || !authed {
			w.Header().Set("WWW-Authenticate", `Basic realm="MacroFlow Sync", charset="UTF-8"`)
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		ctx := contextWithUser(r.Context(), user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) ping(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())

	after, err := parseInt(r.URL.Query().Get("after"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid 'after' cursor")
		return
	}
	limit, err := parseInt(r.URL.Query().Get("limit"), 500)
	if err != nil || limit <= 0 {
		writeError(w, http.StatusBadRequest, "invalid 'limit'")
		return
	}
	if limit > int64(s.cfg.MaxLimit) {
		limit = int64(s.cfg.MaxLimit)
	}

	changes, nextCursor, hasMore, err := s.store.Pull(r.Context(), user, after, int(limit))
	if err != nil {
		s.log.Error("pull failed", "user", user, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Emit an empty array rather than null when there are no changes.
	if changes == nil {
		changes = []store.Change{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"changes":    changes,
		"nextCursor": strconv.FormatInt(nextCursor, 10),
		"hasMore":    hasMore,
	})
}

// pushRequest is the POST /changes body.
type pushRequest struct {
	DeviceID string           `json:"deviceId"`
	Changes  []store.Incoming `json:"changes"`
}

func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())

	var req pushRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if req.DeviceID == "" {
		writeError(w, http.StatusBadRequest, "missing deviceId")
		return
	}
	// Validate every change before touching the DB: a malformed batch must
	// store nothing.
	for _, c := range req.Changes {
		if c.Table == "" || c.RowID == "" {
			writeError(w, http.StatusBadRequest, "change is missing table or rowId")
			return
		}
		if c.Op != "upsert" && c.Op != "delete" {
			writeError(w, http.StatusBadRequest, "change has invalid op")
			return
		}
	}

	accepted, maxSeq, err := s.store.Push(r.Context(), user, req.DeviceID, req.Changes, s.cfg.MaxUserBytes)
	if errors.Is(err, store.ErrQuotaExceeded) {
		// Report the current committed usage so the client can show how far
		// over the cap the account is. Nothing from this batch was stored.
		bytes, _, _ := s.store.Usage(r.Context(), user)
		writeJSON(w, http.StatusInsufficientStorage, map[string]any{
			"error": "storage quota exceeded",
			"bytes": bytes,
			"quota": s.cfg.MaxUserBytes,
		})
		return
	}
	if err != nil {
		s.log.Error("push failed", "user", user, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": accepted,
		"maxSeq":   maxSeq,
	})
}

// usage reports the authenticated user's stored change-log size and the
// configured per-user cap (0 = unlimited).
func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	bytes, rows, err := s.store.Usage(r.Context(), user)
	if err != nil {
		s.log.Error("usage failed", "user", user, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bytes": bytes,
		"rows":  rows,
		"quota": s.cfg.MaxUserBytes,
	})
}

// ── helpers ────────────────────────────────────────────────

func parseInt(raw string, def int64) (int64, error) {
	if raw == "" {
		return def, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
