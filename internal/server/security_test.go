package server

import (
	"context"
	"github.com/koboshchan/kotatsu/internal/api"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTLSAndForwardingPolicy(t *testing.T) {
	s := testServer(t)
	s.Config.Dev = false
	s.Config.PublicURL = "https://shell.example.com"
	request := func(trusted, forwarded bool, path string) int {
		s.Config.TrustedProxies = nil
		if trusted {
			s.Config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
		}
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "192.0.2.1:1234"
		if forwarded {
			r.Header.Set("X-Forwarded-Proto", "https")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if request(false, true, "/") != 400 || request(true, false, "/") != 400 || request(true, true, "/") != 200 || request(false, false, "/healthz") != 200 {
		t.Fatal("TLS policy")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:123"
	r.Header.Set("X-Forwarded-For", "203.0.113.4")
	if s.publicIP(r) != "192.0.2.1" {
		t.Fatal("untrusted forwarding")
	}
	s.Config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if s.publicIP(r) != "203.0.113.4" {
		t.Fatal("trusted forwarding")
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.4, 198.51.100.4")
	if s.publicIP(r) != "192.0.2.1" {
		t.Fatal("non-overwritten header trusted")
	}
}
func TestStaticReleaseAllowlist(t *testing.T) {
	s := testServer(t)
	s.Config.ReleaseVersion = "v0.1.0"
	s.Config.ReleaseDir = t.TempDir()
	os.WriteFile(filepath.Join(s.Config.ReleaseDir, "secret.txt"), []byte("private"), 0600)
	os.WriteFile(filepath.Join(s.Config.ReleaseDir, "version.json"), []byte("{}"), 0600)
	os.Symlink(filepath.Join(s.Config.ReleaseDir, "secret.txt"), filepath.Join(s.Config.ReleaseDir, "SHA256SUMS"))
	for _, tc := range []struct {
		path string
		code int
	}{{"/releases/v0.1.0/version.json", 200}, {"/releases/v0.1.0/secret.txt", 404}, {"/releases/v0.1.0/SHA256SUMS", 404}, {"/releases/v0.2.0/version.json", 404}, {"/releases/v0.1.0/.env", 404}, {"/PLAN.md", 404}} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.code {
			t.Fatal(tc, w.Code)
		}
	}
}
func TestRateLimitAndStoredSecretsNotReturned(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	a, _ := s.Store.Register(ctx, "api-owner")
	e, _ := s.Store.CreateEnrollment(ctx, a.Account.ID, true, 15*time.Minute)
	for _, path := range []string{"/api/v1/tokens", "/api/v1/enrollment-tokens", "/api/v1/machines"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+a.Token.Token)
		s.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), a.Token.Token) || strings.Contains(w.Body.String(), e.Token) {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for n := 0; n < 11; n++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", api.Prefix+"/accounts", strings.NewReader("{}"))
		s.ServeHTTP(w, r)
		if n == 10 && w.Code != 429 {
			t.Fatal("rate limit", w.Code)
		}
	}
}
