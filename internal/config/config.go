// Package config loads the server configuration from environment variables.
package config

import (
	"bufio"
	"crypto/subtle"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the fully-resolved server configuration.
type Config struct {
	// Addr is the TCP address the HTTP server listens on, e.g. ":8080".
	Addr string
	// DBPath is the path to the SQLite database file.
	DBPath string
	// Users maps username -> password. Provisioned out-of-band; there is no
	// signup flow.
	Users map[string]string
	// MaxBodyBytes caps the size of an accepted request body. Push payloads can
	// carry base64 images (progress_photos.image_data), so this must be large.
	MaxBodyBytes int64
	// MaxLimit caps the page size a client may request on pull.
	MaxLimit int
	// AllowSignup enables the self-service POST /api/v1/auth/register endpoint.
	// Disable it on shared deployments once accounts are provisioned.
	AllowSignup bool
}

// Load reads configuration from the environment and validates it.
//
// Recognised variables:
//
//	PORT            listen port                       (default 8080)
//	ADDR            full listen address, overrides PORT
//	DB_PATH         SQLite file path                  (default ./data/macroflow.db)
//	USERS           "alice:secret,bob:hunter2"        (comma/newline separated)
//	USERS_FILE      path to a file of "user:pass" lines (for Docker secrets)
//	MAX_BODY_BYTES  max request body in bytes         (default 33554432 = 32 MiB)
//	MAX_LIMIT       max pull page size                (default 1000)
//	ALLOW_SIGNUP    enable self-service registration  (default true)
//
// At least one of USERS / USERS_FILE must yield a user, otherwise no request
// could ever authenticate.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:         ":" + getenv("PORT", "8080"),
		DBPath:       getenv("DB_PATH", "./data/macroflow.db"),
		Users:        map[string]string{},
		MaxBodyBytes: 32 << 20,
		MaxLimit:     1000,
		AllowSignup:  true,
	}

	if v := os.Getenv("ALLOW_SIGNUP"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid ALLOW_SIGNUP %q (want true/false)", v)
		}
		cfg.AllowSignup = enabled
	}

	if addr := os.Getenv("ADDR"); addr != "" {
		cfg.Addr = addr
	}

	if v := os.Getenv("MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid MAX_BODY_BYTES %q", v)
		}
		cfg.MaxBodyBytes = n
	}

	if v := os.Getenv("MAX_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid MAX_LIMIT %q", v)
		}
		cfg.MaxLimit = n
	}

	if raw := os.Getenv("USERS"); raw != "" {
		if err := parseUsers(raw, cfg.Users); err != nil {
			return nil, err
		}
	}
	if path := os.Getenv("USERS_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading USERS_FILE: %w", err)
		}
		if err := parseUsers(string(data), cfg.Users); err != nil {
			return nil, err
		}
	}

	if len(cfg.Users) == 0 {
		return nil, fmt.Errorf("no users configured: set USERS and/or USERS_FILE")
	}

	return cfg, nil
}

// Authenticate reports whether the given credentials match a configured user.
// The comparison is constant-time to avoid leaking password length/content
// through timing.
func (c *Config) Authenticate(username, password string) bool {
	want, ok := c.Users[username]
	if !ok {
		// Still perform a compare against a fixed string so that unknown and
		// known usernames take a similar amount of time.
		subtle.ConstantTimeCompare([]byte(password), []byte("\x00"))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(want)) == 1
}

// parseUsers accepts "user:pass" entries separated by commas or newlines and
// adds them to dst. Blank lines and lines beginning with '#' are ignored.
func parseUsers(raw string, dst map[string]string) error {
	// Normalise commas to newlines, then scan line by line so passwords may
	// contain characters other than comma.
	scanner := bufio.NewScanner(strings.NewReader(strings.ReplaceAll(raw, ",", "\n")))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, pass, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || pass == "" {
			return fmt.Errorf("invalid user entry %q (want user:pass)", line)
		}
		dst[name] = pass
	}
	return scanner.Err()
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
