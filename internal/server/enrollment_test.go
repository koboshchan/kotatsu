package server

import (
	"context"
	"encoding/json"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/relay"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstallerCredentialSeparation(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	a, _ := s.Store.Register(ctx, "alice")
	e, _ := s.Store.CreateEnrollment(ctx, a.Account.ID, false, time.Minute)
	s.Config.ReleaseVersion = "v0.1.0"
	s.Config.ReleaseURL = "http://localhost"
	for _, tc := range []struct {
		token string
		code  int
	}{{a.Token.Token, 401}, {e.Token, 200}} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/script/"+tc.token, nil))
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("caching installer")
		}
		if tc.code == 200 {
			body := w.Body.String()
			if !strings.Contains(body, "sha256sum -c") || !strings.Contains(body, "User=anyshell") || strings.Contains(body, "%!") {
				t.Fatal("bad installer")
			}
		}
	}
	if s.Store.EnrollmentValid(ctx, e.Token) != nil {
		t.Fatal("fetching script consumed token")
	}
	body, _ := json.Marshal(api.EnrollRequest{Token: e.Token, Name: "test", Platform: "linux/amd64"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/machines/enroll", strings.NewReader(string(body)))
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	s.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out api.Enrolled
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Machine.PublicIP == "1.2.3.4" {
		t.Fatal("untrusted forwarded IP")
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/script/"+e.Token, nil))
	if w.Code != 401 {
		t.Fatal("used token")
	}
}

func TestAgentAuthenticatedBeforeRevocationCannotRegister(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()
	a, _ := s.Store.Register(ctx, "race-owner")
	e, _ := s.Store.CreateEnrollment(ctx, a.Account.ID, false, time.Minute)
	m, err := s.Store.Enroll(ctx, api.EnrollRequest{Token: e.Token, Name: "race"}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	defer srv.Close()
	// Holding the registry lock pauses agent setup after its first authentication
	// and WebSocket upgrade, before final validation/link registration.
	s.mu.Lock()
	c, err := relay.Dial(ctx, srv.URL, api.Prefix+"/agent/connect", m.Credential)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	if err = s.Store.RevokeMachine(ctx, a.Account.ID, m.Machine.DisplayID); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	defer c.CloseNow()
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err = relay.Read(wait, c); err == nil || wait.Err() != nil {
		t.Fatal("revoked in-flight agent link not rejected", err)
	}
	s.mu.Lock()
	link := s.agents[m.Machine.ID]
	s.mu.Unlock()
	if link != nil {
		t.Fatal("revoked link registered")
	}
}
