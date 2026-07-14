package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/herrderkekse/MacroFlow-Backend/internal/api"
	"github.com/herrderkekse/MacroFlow-Backend/internal/config"
	"github.com/herrderkekse/MacroFlow-Backend/internal/store"
)

func newTestServer(t *testing.T) http.Handler {
	return newTestServerWith(t, true)
}

func newTestServerWith(t *testing.T, allowSignup bool) http.Handler {
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
		AllowSignup:  allowSignup,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return api.New(cfg, st, log).Handler()
}

func do(t *testing.T, h http.Handler, method, path, user, pass string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if user != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPingAuth(t *testing.T) {
	h := newTestServer(t)

	if rec := do(t, h, "GET", "/api/v1/sync/ping", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no creds: want 401, got %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/v1/sync/ping", "alice", "wrong", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad creds: want 401, got %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/v1/sync/ping", "alice", "secret", nil); rec.Code != http.StatusOK {
		t.Fatalf("good creds: want 200, got %d", rec.Code)
	}
}

type pullResp struct {
	Changes    []store.Change `json:"changes"`
	NextCursor string         `json:"nextCursor"`
	HasMore    bool           `json:"hasMore"`
}

func pull(t *testing.T, h http.Handler, user, after string) pullResp {
	t.Helper()
	rec := do(t, h, "GET", "/api/v1/sync/changes?after="+after+"&limit=500", user, userPass(user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var pr pullResp
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil {
		t.Fatalf("pull decode: %v", err)
	}
	return pr
}

func userPass(user string) string {
	if user == "bob" {
		return "hunter2"
	}
	return "secret"
}

func push(t *testing.T, h http.Handler, user, device string, changes []map[string]any) {
	t.Helper()
	rec := do(t, h, "POST", "/api/v1/sync/changes", user, userPass(user), map[string]any{
		"deviceId": device,
		"changes":  changes,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("push: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestPushPullRoundTrip(t *testing.T) {
	h := newTestServer(t)

	push(t, h, "alice", "devA", []map[string]any{
		{"table": "foods", "rowId": "f1", "op": "upsert", "updatedAt": 1000, "data": map[string]any{"name": "Oats"}},
		{"table": "entries", "rowId": "e1", "op": "upsert", "updatedAt": 1001, "data": map[string]any{"foodId": "f1"}},
	})

	pr := pull(t, h, "alice", "0")
	if len(pr.Changes) != 2 {
		t.Fatalf("want 2 changes, got %d", len(pr.Changes))
	}
	// seq strictly increasing, order preserved (parent before child).
	if pr.Changes[0].Seq != 1 || pr.Changes[1].Seq != 2 {
		t.Fatalf("unexpected seqs: %d, %d", pr.Changes[0].Seq, pr.Changes[1].Seq)
	}
	if pr.Changes[0].Table != "foods" || pr.Changes[1].Table != "entries" {
		t.Fatalf("order not preserved: %+v", pr.Changes)
	}
	if pr.Changes[0].DeviceID != "devA" {
		t.Fatalf("deviceId not stored: %q", pr.Changes[0].DeviceID)
	}
	var data map[string]any
	if err := json.Unmarshal(pr.Changes[0].Data, &data); err != nil || data["name"] != "Oats" {
		t.Fatalf("data not round-tripped: %v / %v", err, data)
	}
	if pr.NextCursor != "2" || pr.HasMore {
		t.Fatalf("cursor/hasMore wrong: %q %v", pr.NextCursor, pr.HasMore)
	}

	// Pull after the cursor returns nothing but keeps the cursor.
	pr2 := pull(t, h, "alice", "2")
	if len(pr2.Changes) != 0 || pr2.NextCursor != "2" {
		t.Fatalf("empty pull wrong: %+v", pr2)
	}
}

func TestCompactionKeepsLatest(t *testing.T) {
	h := newTestServer(t)

	push(t, h, "alice", "devA", []map[string]any{
		{"table": "foods", "rowId": "f1", "op": "upsert", "updatedAt": 1000, "data": map[string]any{"name": "Oats"}},
	})
	push(t, h, "alice", "devA", []map[string]any{
		{"table": "foods", "rowId": "f1", "op": "upsert", "updatedAt": 2000, "data": map[string]any{"name": "Rolled Oats"}},
	})

	pr := pull(t, h, "alice", "0")
	if len(pr.Changes) != 1 {
		t.Fatalf("compaction: want 1 change, got %d", len(pr.Changes))
	}
	c := pr.Changes[0]
	// seq advanced (never reused) and the latest data won.
	if c.Seq != 2 {
		t.Fatalf("want latest seq 2, got %d", c.Seq)
	}
	if c.UpdatedAt != 2000 {
		t.Fatalf("want latest updatedAt, got %d", c.UpdatedAt)
	}
	var data map[string]any
	json.Unmarshal(c.Data, &data)
	if data["name"] != "Rolled Oats" {
		t.Fatalf("compaction kept stale data: %v", data)
	}

	// A delete tombstone should supersede the upsert and carry no data.
	push(t, h, "alice", "devA", []map[string]any{
		{"table": "foods", "rowId": "f1", "op": "delete", "updatedAt": 3000},
	})
	pr = pull(t, h, "alice", "0")
	if len(pr.Changes) != 1 || pr.Changes[0].Op != "delete" {
		t.Fatalf("delete did not supersede: %+v", pr.Changes)
	}
	if len(pr.Changes[0].Data) != 0 {
		t.Fatalf("delete should carry no data, got %s", pr.Changes[0].Data)
	}
}

func TestUserIsolation(t *testing.T) {
	h := newTestServer(t)
	push(t, h, "alice", "devA", []map[string]any{
		{"table": "foods", "rowId": "f1", "op": "upsert", "updatedAt": 1000, "data": map[string]any{"name": "Oats"}},
	})
	if pr := pull(t, h, "bob", "0"); len(pr.Changes) != 0 {
		t.Fatalf("bob should not see alice's data: %+v", pr.Changes)
	}
}

func TestPagination(t *testing.T) {
	h := newTestServer(t)
	var changes []map[string]any
	for i := 0; i < 5; i++ {
		changes = append(changes, map[string]any{
			"table": "foods", "rowId": string(rune('a' + i)), "op": "upsert", "updatedAt": 1000 + i, "data": map[string]any{},
		})
	}
	push(t, h, "alice", "devA", changes)

	rec := do(t, h, "GET", "/api/v1/sync/changes?after=0&limit=2", "alice", "secret", nil)
	var pr pullResp
	json.Unmarshal(rec.Body.Bytes(), &pr)
	if len(pr.Changes) != 2 || !pr.HasMore || pr.NextCursor != "2" {
		t.Fatalf("page 1 wrong: len=%d hasMore=%v cursor=%s", len(pr.Changes), pr.HasMore, pr.NextCursor)
	}
}

func TestMalformedBatchRejected(t *testing.T) {
	h := newTestServer(t)
	// Invalid op in the batch: nothing should be stored.
	rec := do(t, h, "POST", "/api/v1/sync/changes", "alice", "secret", map[string]any{
		"deviceId": "devA",
		"changes": []map[string]any{
			{"table": "foods", "rowId": "f1", "op": "upsert", "updatedAt": 1, "data": map[string]any{}},
			{"table": "foods", "rowId": "f2", "op": "bogus", "updatedAt": 2},
		},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	if pr := pull(t, h, "alice", "0"); len(pr.Changes) != 0 {
		t.Fatalf("partial batch was stored: %+v", pr.Changes)
	}
}
