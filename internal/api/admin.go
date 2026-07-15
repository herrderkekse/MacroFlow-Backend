// Admin surface: a JSON management API and the embedded dashboard UI, served
// by a separate listener that must only ever be reachable from the host
// itself (see config.AdminAddr). It is deliberately unauthenticated — access
// control is "can you reach the loopback interface", i.e. shell access.
package api

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/herrderkekse/MacroFlow-Backend/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// AdminHandler builds the admin mux: the management API under /api/admin plus
// the embedded dashboard at /.
func (s *Server) AdminHandler(ui fs.FS) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/admin/overview", s.adminOverview)
	mux.HandleFunc("GET /api/admin/users", s.adminUsers)
	mux.HandleFunc("POST /api/admin/users/{username}/password", s.adminSetPassword)
	mux.HandleFunc("DELETE /api/admin/users/{username}/data", s.adminWipeData)
	mux.HandleFunc("DELETE /api/admin/users/{username}", s.adminDeleteAccount)

	// The dashboard is only present when the binary was built after `npm run
	// build` in admin-ui/ (the Dockerfile always does). Point at the API
	// otherwise rather than serving a bare 404.
	if _, err := fs.Stat(ui, "index.html"); err == nil {
		mux.Handle("/", http.FileServerFS(ui))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "admin dashboard was not compiled into this binary "+
				"(run `npm run build` in admin-ui/, then rebuild); "+
				"the JSON API under /api/admin/ works regardless", http.StatusNotFound)
		})
	}

	return mux
}

// adminOverview reports server, storage, and traffic totals for the dashboard
// header cards.
func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	bytes, rows, err := s.store.TotalUsage(r.Context())
	if err != nil {
		s.log.Error("admin: total usage failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.log.Error("admin: listing accounts failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"startedAt":     s.started.UnixMilli(),
		"uptimeSeconds": int64(time.Since(s.started).Seconds()),
		"goVersion":     runtime.Version(),
		"users": map[string]any{
			"static":   len(s.cfg.Users),
			"accounts": len(accounts),
		},
		"storage": map[string]any{
			"bytes":       bytes,
			"rows":        rows,
			"dbFileBytes": dbFileSize(s.cfg.DBPath),
		},
		"requests": map[string]any{
			"total":        s.metrics.total.Load(),
			"clientErrors": s.metrics.err4xx.Load(),
			"serverErrors": s.metrics.err5xx.Load(),
		},
		"config": map[string]any{
			"addr":          s.cfg.Addr,
			"signupAllowed": s.cfg.AllowSignup,
			"maxUserBytes":  s.cfg.MaxUserBytes,
			"maxBodyBytes":  s.cfg.MaxBodyBytes,
		},
	})
}

// adminUser is one row of the dashboard's user table: a credential source
// (static USERS entry or self-service account) merged with its stored-data
// statistics.
type adminUser struct {
	Username     string `json:"username"`
	Source       string `json:"source"` // "static" | "account" | "orphaned"
	CreatedAt    int64  `json:"createdAt,omitempty"`
	Bytes        int64  `json:"bytes"`
	Rows         int64  `json:"rows"`
	Devices      int64  `json:"devices"`
	LastChangeAt int64  `json:"lastChangeAt,omitempty"`
}

// adminUsers lists every known user: all credentials, plus any change log
// whose credential has since disappeared ("orphaned", e.g. a USERS entry that
// was removed from the environment).
func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.AllUserStats(r.Context())
	if err != nil {
		s.log.Error("admin: user stats failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.log.Error("admin: listing accounts failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	users := make([]adminUser, 0, len(s.cfg.Users)+len(accounts))
	take := func(id string) store.UserStats {
		st := stats[id]
		delete(stats, id)
		return st
	}
	for name := range s.cfg.Users {
		st := take(name)
		users = append(users, adminUser{
			Username: name, Source: "static",
			Bytes: st.Bytes, Rows: st.Rows, Devices: st.Devices, LastChangeAt: st.LastChangeAt,
		})
	}
	for _, a := range accounts {
		st := take(a.Username)
		users = append(users, adminUser{
			Username: a.Username, Source: "account", CreatedAt: a.CreatedAt,
			Bytes: st.Bytes, Rows: st.Rows, Devices: st.Devices, LastChangeAt: st.LastChangeAt,
		})
	}
	// Whatever is left in stats has data but no matching credential.
	for id, st := range stats {
		users = append(users, adminUser{
			Username: id, Source: "orphaned",
			Bytes: st.Bytes, Rows: st.Rows, Devices: st.Devices, LastChangeAt: st.LastChangeAt,
		})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })

	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// adminSetPassword replaces a self-service account's password. Static USERS
// entries are configuration, not data, so they get a 409 pointing there.
func (s *Server) adminSetPassword(w http.ResponseWriter, r *http.Request) {
	username, ok := s.adminAccountName(w, r)
	if !ok {
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := validatePassword(body.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		s.log.Error("admin: hashing password failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	switch err := s.store.SetPassword(r.Context(), username, string(hash)); {
	case errors.Is(err, store.ErrUnknownUser):
		writeError(w, http.StatusNotFound, "no such account")
	case err != nil:
		s.log.Error("admin: setting password failed", "user", username, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		s.log.Info("admin: password reset", "user", username)
		writeJSON(w, http.StatusOK, map[string]any{"username": username})
	}
}

// adminWipeData deletes a user's stored change log (any source) but keeps the
// credential, forcing that user's devices to re-push from scratch.
func (s *Server) adminWipeData(w http.ResponseWriter, r *http.Request) {
	username := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	deleted, err := s.store.WipeUserData(r.Context(), username)
	if err != nil {
		s.log.Error("admin: wiping data failed", "user", username, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.log.Info("admin: wiped user data", "user", username, "rows", deleted)
	writeJSON(w, http.StatusOK, map[string]any{"username": username, "deletedRows": deleted})
}

// adminDeleteAccount removes a self-service account and all its data.
func (s *Server) adminDeleteAccount(w http.ResponseWriter, r *http.Request) {
	username, ok := s.adminAccountName(w, r)
	if !ok {
		return
	}
	switch err := s.store.DeleteAccount(r.Context(), username); {
	case errors.Is(err, store.ErrUnknownUser):
		writeError(w, http.StatusNotFound, "no such account")
	case err != nil:
		s.log.Error("admin: deleting account failed", "user", username, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		s.log.Info("admin: deleted account", "user", username)
		writeJSON(w, http.StatusOK, map[string]any{"username": username})
	}
}

// adminAccountName extracts and normalises the {username} path segment for
// account-only endpoints, rejecting static USERS entries with a 409 since they
// can only be managed by editing the environment.
func (s *Server) adminAccountName(w http.ResponseWriter, r *http.Request) (string, bool) {
	username := strings.ToLower(strings.TrimSpace(r.PathValue("username")))
	for existing := range s.cfg.Users {
		if strings.EqualFold(existing, username) {
			writeError(w, http.StatusConflict, "user is provisioned via USERS; edit the environment instead")
			return "", false
		}
	}
	return username, true
}

// dbFileSize returns the on-disk footprint of the SQLite database: the main
// file plus its WAL, which can dwarf the main file between checkpoints. Errors
// (e.g. the WAL not existing) just contribute zero.
func dbFileSize(path string) int64 {
	var total int64
	for _, p := range []string{path, path + "-wal"} {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
		}
	}
	return total
}
