// Contact-form endpoint: unauthenticated (the sender has no account), called
// cross-origin from the public website, so it carries its own CORS headers and
// a per-IP rate limit against drive-by spam.
package api

import (
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
)

// Contact-form field limits. Generous for humans, tight enough that the table
// cannot be ballooned by a single request.
const (
	maxContactNameLen    = 100
	maxContactEmailLen   = 254 // RFC 5321 address ceiling
	maxContactSubjectLen = 200
	maxContactMessageLen = 5000
)

// contactTypes are the known submitting forms. Extend when the website grows
// another form so the admin can tell submissions apart.
var contactTypes = map[string]bool{
	"contact": true,
	"support": true,
}

// Rate-limit window for contact submissions, per client IP.
const (
	contactRateLimit  = 5
	contactRateWindow = 15 * time.Minute
)

// contactRequest is the POST /api/v1/contact body.
type contactRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Subject string `json:"subject"`
	Message string `json:"message"`
}

func (s *Server) contact(w http.ResponseWriter, r *http.Request) {
	var req contactRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	kind := strings.TrimSpace(req.Type)
	name := strings.TrimSpace(req.Name)
	email := strings.TrimSpace(req.Email)
	subject := strings.TrimSpace(req.Subject)
	message := strings.TrimSpace(req.Message)

	switch {
	case !contactTypes[kind]:
		writeError(w, http.StatusBadRequest, "unknown message type")
		return
	case name == "" || len(name) > maxContactNameLen:
		writeError(w, http.StatusBadRequest, "name is required (max 100 characters)")
		return
	case email == "" || len(email) > maxContactEmailLen:
		writeError(w, http.StatusBadRequest, "email is required (max 254 characters)")
		return
	case subject == "" || len(subject) > maxContactSubjectLen:
		writeError(w, http.StatusBadRequest, "subject is required (max 200 characters)")
		return
	case message == "" || len(message) > maxContactMessageLen:
		writeError(w, http.StatusBadRequest, "message is required (max 5000 characters)")
		return
	}
	if _, err := mail.ParseAddress(email); err != nil {
		writeError(w, http.StatusBadRequest, "invalid email address")
		return
	}

	if !s.contactLimiter.allow(clientIP(r), time.Now()) {
		writeError(w, http.StatusTooManyRequests, "too many messages, please try again later")
		return
	}

	if _, err := s.store.AddContactMessage(r.Context(), kind, name, email, subject, message, time.Now().UnixMilli()); err != nil {
		s.log.Error("storing contact message failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.log.Info("contact message received", "type", kind, "email", email)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// cors wraps a handler with the CORS headers the browser needs to call this
// API from the website's origin, and answers preflight requests. Allowed
// origins come from cfg.CORSOrigins ("*" allows any).
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	for _, allowed := range s.cfg.CORSOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

// ipLimiter is a fixed-window per-IP rate limiter. Windows are tracked
// in-memory (they reset on restart), and stale entries are pruned whenever a
// new window starts so the map cannot grow without bound.
type ipLimiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	windows map[string]*ipWindow
}

type ipWindow struct {
	start time.Time
	count int
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, windows: map[string]*ipWindow{}}
}

// allow reports whether ip may perform another request at time now, counting
// it if so.
func (l *ipLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.windows[ip]
	if w == nil || now.Sub(w.start) >= l.window {
		l.prune(now)
		l.windows[ip] = &ipWindow{start: now, count: 1}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}

// prune drops expired windows. Called with mu held.
func (l *ipLimiter) prune(now time.Time) {
	for ip, w := range l.windows {
		if now.Sub(w.start) >= l.window {
			delete(l.windows, ip)
		}
	}
}

// clientIP extracts the peer address. RemoteAddr is deliberate: honouring
// X-Forwarded-For would let any client forge its identity unless we knew the
// deployment's proxy chain. Behind a reverse proxy this limits per-proxy, not
// per-client, which is still an acceptable spam cap for a contact form.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
