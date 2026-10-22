package server

import (
	"github.com/koboshchan/kotatsu/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	db, e := store.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	s, e := New(db, Config{PublicURL: "http://localhost", Dev: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestRegistrationAndAuthorization(t *testing.T) {
	s := testServer(t)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/accounts", `{"name":"alice"}`, 201},
		{"POST", "/api/v1/accounts", `{"name":"alice"}`, 409},
		{"POST", "/api/v1/accounts", `{"name":"","extra":true}`, 400},
		{"GET", "/api/v1/me", "", 401},
		{"GET", "/api/v1/tokens", "", 401},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		s.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(test, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache")
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(`{"name":"mallory"}`))
	r.Header.Set("Origin", "https://evil.test")
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
