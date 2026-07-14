package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herrderkekse/MacroFlow-Backend/internal/api"
	"github.com/herrderkekse/MacroFlow-Backend/internal/config"
	"github.com/herrderkekse/MacroFlow-Backend/internal/store"
)

// newCappedServer builds a server with a per-user storage cap.
func newCappedServer(t *testing.T, maxUserBytes int64) http.Handler {
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
		MaxUserBytes: maxUserBytes,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return api.New(cfg, st, log).Handler()
}

type usageResp struct {
	Bytes int64 `json:"bytes"`
	Rows  int64 `json:"rows"`
	Quota int64 `json:"quota"`
}

func getUsage(t *testing.T, h http.Handler, user string) usageResp {
	t.Helper()
	rec := do(t, h, "GET", "/api/v1/sync/usage", user, userPass(user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var ur usageResp
	if err := json.Unmarshal(rec.Body.Bytes(), &ur); err != nil {
		t.Fatalf("usage decode: %v", err)
	}
	return ur
}

func TestUsageReportsStoredBytes(t *testing.T) {
	h := newTestServer(t) // no cap → quota 0

	if ur := getUsage(t, h, "alice"); ur.Bytes != 0 || ur.Rows != 0 || ur.Quota != 0 {
		t.Fatalf("empty usage wrong: %+v", ur)
	}

	push(t, h, "alice", "devA", []map[string]any{
		{"table": "foods", "rowId": "f1", "op": "upsert", "updatedAt": 1000, "data": map[string]any{"name": "Oats"}},
	})
	ur := getUsage(t, h, "alice")
	if ur.Rows != 1 || ur.Bytes <= 0 {
		t.Fatalf("usage after push wrong: %+v", ur)
	}
	// Isolation: bob sees his own (empty) usage.
	if ub := getUsage(t, h, "bob"); ub.Bytes != 0 || ub.Rows != 0 {
		t.Fatalf("bob usage should be empty: %+v", ub)
	}
}

func TestUsageCompactionShrinks(t *testing.T) {
	h := newTestServer(t)
	big := strings.Repeat("x", 2000)
	push(t, h, "alice", "devA", []map[string]any{
		{"table": "photos", "rowId": "p1", "op": "upsert", "updatedAt": 1, "data": map[string]any{"img": big}},
	})
	before := getUsage(t, h, "alice").Bytes

	// Deleting the row (tombstone carries no data) must reduce usage.
	push(t, h, "alice", "devA", []map[string]any{
		{"table": "photos", "rowId": "p1", "op": "delete", "updatedAt": 2},
	})
	after := getUsage(t, h, "alice").Bytes
	if !(after < before) {
		t.Fatalf("delete did not shrink usage: before=%d after=%d", before, after)
	}
}

func TestQuotaRejectsOversizePush(t *testing.T) {
	h := newCappedServer(t, 500)

	// A single change well over the cap is rejected with 507, and nothing is
	// stored.
	big := strings.Repeat("x", 1000)
	rec := do(t, h, "POST", "/api/v1/sync/changes", "alice", "secret", map[string]any{
		"deviceId": "devA",
		"changes": []map[string]any{
			{"table": "photos", "rowId": "p1", "op": "upsert", "updatedAt": 1, "data": map[string]any{"img": big}},
		},
	})
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("oversize push: want 507, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
		Quota int64  `json:"quota"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Quota != 500 {
		t.Fatalf("507 body should report quota 500, got %d", body.Quota)
	}
	if ur := getUsage(t, h, "alice"); ur.Rows != 0 {
		t.Fatalf("rejected batch was stored: %+v", ur)
	}
}

func TestQuotaAllowsDeleteWhenOver(t *testing.T) {
	h := newCappedServer(t, 400)

	// Fill just under the cap.
	push(t, h, "alice", "devA", []map[string]any{
		{"table": "photos", "rowId": "p1", "op": "upsert", "updatedAt": 1, "data": map[string]any{"img": strings.Repeat("x", 300)}},
	})
	// A further upsert would exceed the cap → rejected.
	rec := do(t, h, "POST", "/api/v1/sync/changes", "alice", "secret", map[string]any{
		"deviceId": "devA",
		"changes": []map[string]any{
			{"table": "photos", "rowId": "p2", "op": "upsert", "updatedAt": 2, "data": map[string]any{"img": strings.Repeat("y", 300)}},
		},
	})
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("over-cap upsert: want 507, got %d", rec.Code)
	}
	// But deleting the existing row frees space and is accepted even though the
	// account was at the cap.
	rec = do(t, h, "POST", "/api/v1/sync/changes", "alice", "secret", map[string]any{
		"deviceId": "devA",
		"changes": []map[string]any{
			{"table": "photos", "rowId": "p1", "op": "delete", "updatedAt": 3},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete under cap: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}
