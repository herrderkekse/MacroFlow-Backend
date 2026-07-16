package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContactSubmit(t *testing.T) {
	h := newTestServer(t)

	rec := do(t, h, "POST", "/api/v1/contact", "", "", map[string]any{
		"type": "contact", "name": "Jane Doe", "email": "jane@example.com",
		"subject": "Feedback", "message": "Hello there!",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid submission: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestContactValidation(t *testing.T) {
	h := newTestServer(t)

	valid := func(overrides map[string]any) map[string]any {
		body := map[string]any{
			"type": "contact", "name": "Jane", "email": "a@b.de",
			"subject": "Hi", "message": "hi",
		}
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
		{"missing type", valid(map[string]any{"type": nil})},
		{"unknown type", valid(map[string]any{"type": "billing"})},
		{"missing name", valid(map[string]any{"name": nil})},
		{"missing email", valid(map[string]any{"email": nil})},
		{"missing subject", valid(map[string]any{"subject": nil})},
		{"missing message", valid(map[string]any{"message": nil})},
		{"invalid email", valid(map[string]any{"email": "not-an-email"})},
		{"subject too long", valid(map[string]any{"subject": strings.Repeat("x", 201)})},
		{"message too long", valid(map[string]any{"message": strings.Repeat("x", 5001)})},
		{"unknown field", valid(map[string]any{"extra": true})},
	}
	for _, tc := range cases {
		if rec := do(t, h, "POST", "/api/v1/contact", "", "", tc.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d (%s)", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestContactRateLimit(t *testing.T) {
	h := newTestServer(t)
	body := map[string]any{
		"type": "support", "name": "Jane", "email": "a@b.de",
		"subject": "Hi", "message": "hi",
	}

	// httptest requests all share the same RemoteAddr, so they count against
	// one window.
	for i := 0; i < 5; i++ {
		if rec := do(t, h, "POST", "/api/v1/contact", "", "", body); rec.Code != http.StatusCreated {
			t.Fatalf("submission %d: want 201, got %d (%s)", i+1, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, h, "POST", "/api/v1/contact", "", "", body); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th submission: want 429, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestContactCORS(t *testing.T) {
	h := newTestServer(t)

	// Preflight from the allowed origin.
	req := httptest.NewRequest("OPTIONS", "/api/v1/contact", nil)
	req.Header.Set("Origin", "https://macro-flow.org")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: want 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://macro-flow.org" {
		t.Fatalf("preflight allow-origin: want the requesting origin, got %q", got)
	}

	// A disallowed origin gets no CORS grant.
	req = httptest.NewRequest("OPTIONS", "/api/v1/contact", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin: want no allow-origin header, got %q", got)
	}
}
