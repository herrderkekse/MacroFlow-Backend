package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestShareCreateRequiresAuth(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, "POST", "/api/v1/share", "", "", map[string]any{
		"kind": "food", "version": 1, "payload": map[string]any{"name": "Oats"},
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no creds: want 401, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestShareCreateValidation(t *testing.T) {
	h := newTestServer(t)

	valid := func(overrides map[string]any) map[string]any {
		body := map[string]any{"kind": "food", "version": 1, "payload": map[string]any{"name": "Oats"}}
		for k, v := range overrides {
			if v == nil {
				delete(body, k)
			} else {
				body[k] = v
			}
		}
		return body
	}
	cases := []struct {
		name string
		body map[string]any
	}{
		{"unknown kind", valid(map[string]any{"kind": "workout"})},
		{"missing kind", valid(map[string]any{"kind": nil})},
		{"unsupported version", valid(map[string]any{"version": 2})},
		{"missing payload", valid(map[string]any{"payload": nil})},
	}
	for _, tc := range cases {
		if rec := do(t, h, "POST", "/api/v1/share", "alice", "secret", tc.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d (%s)", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestShareCreateTooLarge(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, "POST", "/api/v1/share", "alice", "secret", map[string]any{
		"kind": "food", "version": 1,
		"payload": map[string]any{"name": strings.Repeat("x", 70_000)},
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized payload: want 413, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestShareRoundTrip(t *testing.T) {
	h := newTestServer(t)

	rec := do(t, h, "POST", "/api/v1/share", "alice", "secret", map[string]any{
		"kind": "food", "version": 1,
		"payload": map[string]any{"name": "Oats", "caloriesPer100g": 389},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	var created struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Token == "" || !strings.HasSuffix(created.URL, "/s/"+created.Token) {
		t.Fatalf("unexpected create response: %+v", created)
	}

	rec = do(t, h, "GET", "/api/v1/share/"+created.Token, "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Kind    string         `json:"kind"`
		Version int            `json:"version"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode fetch response: %v", err)
	}
	if got.Kind != "food" || got.Version != 1 || got.Payload["name"] != "Oats" {
		t.Fatalf("payload not round-tripped: %+v", got)
	}
}

func TestShareRedirect(t *testing.T) {
	h := newTestServer(t)

	rec := do(t, h, "POST", "/api/v1/share", "alice", "secret", map[string]any{
		"kind": "recipe", "version": 1, "payload": map[string]any{"name": "Chili"},
	})
	var created struct{ Token string }
	json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, h, "GET", "/s/"+created.Token, "", "", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect: want 302, got %d (%s)", rec.Code, rec.Body.String())
	}
	// The origin rides along so the receiving app knows which server to fetch
	// the payload from (httptest requests carry Host "example.com").
	want := "macroflow://share/" + created.Token + "?origin=" + url.QueryEscape("http://example.com")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("redirect location: want %q, got %q", want, got)
	}
}

func TestShareNotFound(t *testing.T) {
	h := newTestServer(t)

	if rec := do(t, h, "GET", "/api/v1/share/does-not-exist", "", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("fetch missing: want 404, got %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/s/does-not-exist", "", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("redirect missing: want 404, got %d", rec.Code)
	}
}
