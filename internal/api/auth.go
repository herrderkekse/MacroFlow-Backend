package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/herrderkekse/MacroFlow-Backend/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Account credential limits. The password ceiling is bcrypt's hard 72-byte
// input limit: anything beyond it is silently ignored by bcrypt, so we reject
// it rather than let a longer password appear to "work" while only its first
// 72 bytes matter.
const (
	minUsernameLen = 3
	maxUsernameLen = 64
	minPasswordLen = 8
	maxPasswordLen = 72
)

// credentials is the body for both register and login.
type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// register creates a new self-service account. It is unauthenticated (the
// caller has no account yet) but body-size capped by the surrounding
// middleware. Usernames are case-insensitive and stored lower-cased.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowSignup {
		writeError(w, http.StatusForbidden, "signup is disabled on this server")
		return
	}

	creds, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	username, err := normaliseUsername(creds.Username)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePassword(creds.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A DB account must not shadow a statically-provisioned USERS entry
	// (compared case-insensitively since DB names are lower-cased).
	for existing := range s.cfg.Users {
		if strings.EqualFold(existing, username) {
			writeError(w, http.StatusConflict, "username is already taken")
			return
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(creds.Password), bcrypt.DefaultCost)
	if err != nil {
		s.log.Error("hashing password failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	switch err := s.store.CreateUser(r.Context(), username, string(hash), time.Now().UnixMilli()); {
	case errors.Is(err, store.ErrUserExists):
		writeError(w, http.StatusConflict, "username is already taken")
	case err != nil:
		s.log.Error("creating user failed", "user", username, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"username": username})
	}
}

// login verifies an account's credentials. It is the explicit counterpart to
// the app's "Sign In": the client then reuses these credentials for Basic-auth
// sync. Unknown users and wrong passwords are reported identically.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	creds, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	user, authed := s.authenticate(r.Context(), creds.Username, creds.Password)
	if !authed {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": user})
}

// authenticate checks username/password against either a static USERS entry
// (exact, constant-time compare) or a stored DB account (case-insensitive,
// bcrypt) and returns the canonical user id to key the change log by. It is the
// single credential check shared by login and the Basic-auth middleware.
func (s *Server) authenticate(ctx context.Context, username, password string) (canonical string, ok bool) {
	// Static USERS keep their original case-sensitive, plaintext-compare
	// behaviour so existing deployments are unaffected.
	if s.cfg.Authenticate(username, password) {
		return username, true
	}

	name := strings.ToLower(strings.TrimSpace(username))
	hash, found, err := s.store.UserPasswordHash(ctx, name)
	if err != nil {
		s.log.Error("looking up user failed", "user", name, "err", err)
		return "", false
	}
	if !found {
		// Still run a bcrypt compare so unknown and known users take a similar
		// amount of time, limiting username enumeration by timing.
		bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password))
		return "", false
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", false
	}
	return name, true
}

// dummyHash is a valid bcrypt hash of a throwaway password, compared against
// when a username is unknown so the timing matches the found-user path.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// decodeCredentials reads and JSON-decodes the request body, writing an error
// response and returning ok=false on failure.
func decodeCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var creds credentials
	return creds, decodeJSON(w, r, &creds)
}

// normaliseUsername trims, lower-cases, and validates a username. Allowed
// characters are letters, digits, and the separators '.', '_', '-'.
func normaliseUsername(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if len(name) < minUsernameLen || len(name) > maxUsernameLen {
		return "", errUsernameLength
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-", r) {
			return "", errUsernameChars
		}
	}
	return name, nil
}

func validatePassword(pw string) error {
	if len(pw) < minPasswordLen {
		return errPasswordShort
	}
	if len(pw) > maxPasswordLen {
		return errPasswordLong
	}
	return nil
}

var (
	errUsernameLength = &validationError{"username must be 3–64 characters"}
	errUsernameChars  = &validationError{"username may contain only letters, digits, '.', '_' and '-'"}
	errPasswordShort  = &validationError{"password must be at least 8 characters"}
	errPasswordLong   = &validationError{"password must be at most 72 bytes"}
)

// validationError is a small typed error so its message can be returned to the
// client verbatim.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }
