package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// register posts to the register endpoint (unauthenticated) and returns the
// recorder for status assertions.
func register(t *testing.T, h http.Handler, username, password string) int {
	t.Helper()
	rec := do(t, h, "POST", "/api/v1/auth/register", "", "", map[string]any{
		"username": username, "password": password,
	})
	return rec.Code
}

func login(t *testing.T, h http.Handler, username, password string) int {
	t.Helper()
	rec := do(t, h, "POST", "/api/v1/auth/login", "", "", map[string]any{
		"username": username, "password": password,
	})
	return rec.Code
}

func TestRegisterThenLogin(t *testing.T) {
	h := newTestServer(t)

	if code := register(t, h, "carol", "supersecret"); code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", code)
	}
	if code := login(t, h, "carol", "supersecret"); code != http.StatusOK {
		t.Fatalf("login: want 200, got %d", code)
	}
	if code := login(t, h, "carol", "wrongpass"); code != http.StatusUnauthorized {
		t.Fatalf("login wrong password: want 401, got %d", code)
	}
	if code := login(t, h, "nobody", "supersecret"); code != http.StatusUnauthorized {
		t.Fatalf("login unknown user: want 401, got %d", code)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	h := newTestServer(t)
	if code := register(t, h, "dave", "supersecret"); code != http.StatusCreated {
		t.Fatalf("first register: want 201, got %d", code)
	}
	if code := register(t, h, "dave", "othersecret"); code != http.StatusConflict {
		t.Fatalf("duplicate register: want 409, got %d", code)
	}
	// Case-insensitive: DAVE collides with dave.
	if code := register(t, h, "DAVE", "othersecret"); code != http.StatusConflict {
		t.Fatalf("case-insensitive duplicate: want 409, got %d", code)
	}
}

func TestRegisterShadowsStaticUser(t *testing.T) {
	h := newTestServer(t)
	// "alice" is a statically-provisioned USERS entry in the test config.
	if code := register(t, h, "alice", "supersecret"); code != http.StatusConflict {
		t.Fatalf("shadowing static user: want 409, got %d", code)
	}
}

func TestRegisterValidation(t *testing.T) {
	h := newTestServer(t)
	cases := []struct {
		name, user, pass string
	}{
		{"short username", "ab", "supersecret"},
		{"bad chars", "carol!", "supersecret"},
		{"short password", "carol", "short"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code := register(t, h, c.user, c.pass); code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", code)
			}
		})
	}
}

func TestRegisteredUserCanSync(t *testing.T) {
	h := newTestServer(t)
	if code := register(t, h, "erin", "supersecret"); code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", code)
	}

	// The registered account authenticates against the Basic-auth sync routes.
	if rec := do(t, h, "GET", "/api/v1/sync/ping", "erin", "supersecret", nil); rec.Code != http.StatusOK {
		t.Fatalf("registered user ping: want 200, got %d", rec.Code)
	}
	// Casing sent over Basic auth does not matter for DB accounts.
	if rec := do(t, h, "GET", "/api/v1/sync/ping", "Erin", "supersecret", nil); rec.Code != http.StatusOK {
		t.Fatalf("mixed-case ping: want 200, got %d", rec.Code)
	}

	// Push as "Erin" (mixed case) and pull as "erin": both resolve to the same
	// canonical user id, so the change is visible.
	if rec := do(t, h, "POST", "/api/v1/sync/changes", "Erin", "supersecret", map[string]any{
		"deviceId": "devZ",
		"changes": []map[string]any{
			{"table": "foods", "rowId": "f1", "op": "upsert", "updatedAt": 1000, "data": map[string]any{"name": "Kale"}},
		},
	}); rec.Code != http.StatusOK {
		t.Fatalf("registered user push: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec := do(t, h, "GET", "/api/v1/sync/changes?after=0&limit=500", "erin", "supersecret", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("registered user pull: want 200, got %d", rec.Code)
	}
	var pr pullResp
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil {
		t.Fatalf("pull decode: %v", err)
	}
	if len(pr.Changes) != 1 {
		t.Fatalf("registered user sync: want 1 change, got %d", len(pr.Changes))
	}
}

func TestSignupDisabled(t *testing.T) {
	h := newTestServerWith(t, false)
	if code := register(t, h, "frank", "supersecret"); code != http.StatusForbidden {
		t.Fatalf("register with signup disabled: want 403, got %d", code)
	}
}

func TestLoginStaticUser(t *testing.T) {
	h := newTestServer(t)
	// Statically-provisioned users can log in too.
	if code := login(t, h, "alice", "secret"); code != http.StatusOK {
		t.Fatalf("static user login: want 200, got %d", code)
	}
	// And the returned username echoes what was sent.
	rec := do(t, h, "POST", "/api/v1/auth/login", "", "", map[string]any{"username": "alice", "password": "secret"})
	var body struct {
		Username string `json:"username"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Username != "alice" {
		t.Fatalf("login username echo: want alice, got %q", body.Username)
	}
}
