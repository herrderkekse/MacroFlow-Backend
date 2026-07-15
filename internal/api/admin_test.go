package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/herrderkekse/MacroFlow-Backend/internal/api"
	"github.com/herrderkekse/MacroFlow-Backend/internal/config"
	"github.com/herrderkekse/MacroFlow-Backend/internal/store"
)

// newAdminTestServer returns the public and admin handlers backed by the same
// server, plus the store for seeding data that can't come through the public
// API (e.g. orphaned change logs).
func newAdminTestServer(t *testing.T) (st *store.Store, public, admin http.Handler) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		Users:        map[string]string{"alice": "secret", "bob": "hunter2"},
		MaxBodyBytes: 32 << 20,
		MaxLimit:     1000,
		AllowSignup:  true,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := api.New(cfg, st, log)
	return st, srv.Handler(), srv.AdminHandler(fstest.MapFS{})
}

// pushOne stores a single upsert for user via the public API.
func pushOne(t *testing.T, public http.Handler, user, pass, rowID string) {
	t.Helper()
	rec := do(t, public, "POST", "/api/v1/sync/changes", user, pass, map[string]any{
		"deviceId": "dev-1",
		"changes": []map[string]any{{
			"table": "meals", "rowId": rowID, "op": "upsert",
			"updatedAt": 1700000000000, "data": map[string]any{"kcal": 500},
		}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("push: want 200, got %d: %s", rec.Code, rec.Body)
	}
}

func decode[T any](t *testing.T, body io.Reader) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return v
}

func TestAdminOverview(t *testing.T) {
	_, public, admin := newAdminTestServer(t)

	pushOne(t, public, "alice", "secret", "row-1")
	if rec := do(t, public, "POST", "/api/v1/auth/register", "", "", map[string]string{
		"username": "carol", "password": "password9",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", rec.Code)
	}
	// A 401 should land in the client-error counter; /healthz must not count.
	do(t, public, "GET", "/api/v1/sync/ping", "alice", "wrong", nil)
	do(t, public, "GET", "/healthz", "", "", nil)

	rec := do(t, admin, "GET", "/api/admin/overview", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: want 200, got %d: %s", rec.Code, rec.Body)
	}
	ov := decode[struct {
		Users    struct{ Static, Accounts int }
		Storage  struct{ Bytes, Rows int64 }
		Requests struct{ Total, ClientErrors, ServerErrors int64 }
	}](t, rec.Body)

	if ov.Users.Static != 2 || ov.Users.Accounts != 1 {
		t.Errorf("users: want 2 static / 1 account, got %+v", ov.Users)
	}
	if ov.Storage.Rows != 1 || ov.Storage.Bytes <= 0 {
		t.Errorf("storage: want 1 row and >0 bytes, got %+v", ov.Storage)
	}
	// push + register + failed ping = 3 counted; healthz excluded.
	if ov.Requests.Total != 3 || ov.Requests.ClientErrors != 1 || ov.Requests.ServerErrors != 0 {
		t.Errorf("requests: want total 3 / 4xx 1 / 5xx 0, got %+v", ov.Requests)
	}
}

func TestAdminUsers(t *testing.T) {
	st, public, admin := newAdminTestServer(t)

	pushOne(t, public, "alice", "secret", "row-1")
	if rec := do(t, public, "POST", "/api/v1/auth/register", "", "", map[string]string{
		"username": "carol", "password": "password9",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", rec.Code)
	}
	// A change log whose credential no longer exists (e.g. a USERS entry that
	// was removed from the environment).
	if _, _, err := st.Push(context.Background(), "ghost", "dev-9", []store.Incoming{
		{Table: "meals", RowID: "r", Op: "upsert", UpdatedAt: 1},
	}, 0); err != nil {
		t.Fatalf("seeding orphan: %v", err)
	}

	rec := do(t, admin, "GET", "/api/admin/users", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("users: want 200, got %d", rec.Code)
	}
	resp := decode[struct {
		Users []struct {
			Username, Source string
			Rows             int64
		}
	}](t, rec.Body)

	want := map[string]string{
		"alice": "static", "bob": "static", "carol": "account", "ghost": "orphaned",
	}
	if len(resp.Users) != len(want) {
		t.Fatalf("want %d users, got %d: %+v", len(want), len(resp.Users), resp.Users)
	}
	for _, u := range resp.Users {
		if want[u.Username] != u.Source {
			t.Errorf("user %q: want source %q, got %q", u.Username, want[u.Username], u.Source)
		}
		if u.Username == "alice" && u.Rows != 1 {
			t.Errorf("alice: want 1 row, got %d", u.Rows)
		}
	}
}

func TestAdminSetPassword(t *testing.T) {
	_, public, admin := newAdminTestServer(t)

	if rec := do(t, public, "POST", "/api/v1/auth/register", "", "", map[string]string{
		"username": "carol", "password": "oldpassword",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", rec.Code)
	}

	if rec := do(t, admin, "POST", "/api/admin/users/carol/password", "", "",
		map[string]string{"password": "newpassword"}); rec.Code != http.StatusOK {
		t.Fatalf("reset: want 200, got %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, public, "GET", "/api/v1/sync/ping", "carol", "newpassword", nil); rec.Code != http.StatusOK {
		t.Errorf("new password: want 200, got %d", rec.Code)
	}
	if rec := do(t, public, "GET", "/api/v1/sync/ping", "carol", "oldpassword", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("old password: want 401, got %d", rec.Code)
	}

	if rec := do(t, admin, "POST", "/api/admin/users/alice/password", "", "",
		map[string]string{"password": "newpassword"}); rec.Code != http.StatusConflict {
		t.Errorf("static user: want 409, got %d", rec.Code)
	}
	if rec := do(t, admin, "POST", "/api/admin/users/nobody/password", "", "",
		map[string]string{"password": "newpassword"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown user: want 404, got %d", rec.Code)
	}
	if rec := do(t, admin, "POST", "/api/admin/users/carol/password", "", "",
		map[string]string{"password": "short"}); rec.Code != http.StatusBadRequest {
		t.Errorf("short password: want 400, got %d", rec.Code)
	}
}

func TestAdminWipeData(t *testing.T) {
	_, public, admin := newAdminTestServer(t)

	pushOne(t, public, "alice", "secret", "row-1")
	pushOne(t, public, "alice", "secret", "row-2")

	rec := do(t, admin, "DELETE", "/api/admin/users/alice/data", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("wipe: want 200, got %d: %s", rec.Code, rec.Body)
	}
	if got := decode[struct{ DeletedRows int64 }](t, rec.Body); got.DeletedRows != 2 {
		t.Errorf("want 2 deleted rows, got %d", got.DeletedRows)
	}

	// The seq counter must survive a wipe so cursors held by devices stay
	// valid: the next push continues counting, it doesn't restart at 1.
	pushOne(t, public, "alice", "secret", "row-3")
	pull := do(t, public, "GET", "/api/v1/sync/changes?after=0", "alice", "secret", nil)
	changes := decode[struct{ Changes []struct{ Seq int64 } }](t, pull.Body)
	if len(changes.Changes) != 1 || changes.Changes[0].Seq != 3 {
		t.Errorf("post-wipe push: want single change with seq 3, got %+v", changes.Changes)
	}
}

func TestAdminDeleteAccount(t *testing.T) {
	_, public, admin := newAdminTestServer(t)

	if rec := do(t, public, "POST", "/api/v1/auth/register", "", "", map[string]string{
		"username": "carol", "password": "password9",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("register: want 201, got %d", rec.Code)
	}
	pushOne(t, public, "carol", "password9", "row-1")

	if rec := do(t, admin, "DELETE", "/api/admin/users/carol", "", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, public, "GET", "/api/v1/sync/ping", "carol", "password9", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("deleted account: want 401, got %d", rec.Code)
	}

	// No trace left: neither the account nor an orphaned change log.
	rec := do(t, admin, "GET", "/api/admin/users", "", "", nil)
	users := decode[struct{ Users []struct{ Username string } }](t, rec.Body)
	for _, u := range users.Users {
		if u.Username == "carol" {
			t.Errorf("carol still listed after delete: %+v", users.Users)
		}
	}

	if rec := do(t, admin, "DELETE", "/api/admin/users/alice", "", "", nil); rec.Code != http.StatusConflict {
		t.Errorf("static user: want 409, got %d", rec.Code)
	}
	if rec := do(t, admin, "DELETE", "/api/admin/users/nobody", "", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown user: want 404, got %d", rec.Code)
	}
}

func TestAdminUIFallback(t *testing.T) {
	_, _, admin := newAdminTestServer(t) // fstest.MapFS{} — UI not built

	rec := do(t, admin, "GET", "/", "", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 without built UI, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "admin-ui") {
		t.Errorf("fallback should point at admin-ui, got: %s", rec.Body)
	}
}
