package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/auth"
	"github.com/koboshchan/anyshell/internal/config"
	"github.com/koboshchan/anyshell/internal/store"
	"golang.org/x/time/rate"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	PublicURL      string
	Dev            bool
	TrustedProxies []netip.Prefix
	ReleaseVersion string
	ReleaseURL     string
	ReleaseDir     string
}
type bucket struct {
	limiter *rate.Limiter
	seen    time.Time
}
type Server struct {
	Store  *store.Store
	Config Config
	Log    *slog.Logger
	mux    *http.ServeMux
	mu     sync.Mutex
	limits map[string]*bucket
}
type identityKey struct{}

func New(s *store.Store, c Config, log *slog.Logger) (*Server, error) {
	if err := config.ValidateURL(c.PublicURL, c.Dev); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	v := &Server{Store: s, Config: c, Log: log, mux: http.NewServeMux(), limits: map[string]*bucket{}}
	v.routes()
	return v, nil
}
func (s *Server) routes() {
	s.enrollmentRoutes()
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	s.mux.HandleFunc("POST "+api.Prefix+"/accounts", s.register)
	s.handle("GET "+api.Prefix+"/me", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, identity(r).Account) })
	s.handle("GET "+api.Prefix+"/tokens", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Tokens(r.Context(), identity(r).Account.ID)
		result(w, v, e)
	})
	s.handle("POST "+api.Prefix+"/tokens", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.IssueToken(r.Context(), identity(r).Account.ID, "")
		result(w, v, e)
	})
	s.handle("POST "+api.Prefix+"/tokens/rotate", func(w http.ResponseWriter, r *http.Request) {
		i := identity(r)
		v, e := s.Store.IssueToken(r.Context(), i.Account.ID, i.TokenID)
		result(w, v, e)
	})
	s.handle("DELETE "+api.Prefix+"/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := s.Store.RevokeToken(r.Context(), identity(r).Account.ID, r.PathValue("id"))
		result(w, map[string]bool{"revoked": true}, e)
	})
}
func (s *Server) handle(pattern string, h http.HandlerFunc) { s.mux.Handle(pattern, s.account(h)) }
func identity(r *http.Request) store.Identity {
	return r.Context().Value(identityKey{}).(store.Identity)
}
func (s *Server) account(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := auth.Bearer(r.Header.Get("Authorization"), "as")
		if token == "" {
			fail(w, 401, "account bearer token required")
			return
		}
		i, err := s.Store.Authenticate(r.Context(), token)
		if err != nil {
			fail(w, 401, "invalid or revoked account token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, i)))
	}
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if !s.Config.Dev && r.TLS == nil && !(s.trusted(r) && r.Header.Get("X-Forwarded-Proto") == "https") {
		fail(w, 400, "HTTPS required")
		return
	}
	// Do not log raw paths, headers or bodies: the installer URL is a credential.
	if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.Config.PublicURL {
		fail(w, 403, "cross-origin request rejected")
		return
	}
	s.mux.ServeHTTP(w, r)
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		fail(w, 400, "invalid JSON")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "expected one JSON value")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) { respond(w, status, api.Error{Error: msg}) }
func result(w http.ResponseWriter, v any, err error) {
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, 404, "not found")
		} else if errors.Is(err, store.ErrUnauthorized) {
			fail(w, 401, "invalid credential")
		} else {
			fail(w, 500, "internal error")
		}
		return
	}
	respond(w, 200, v)
}
func (s *Server) trusted(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, p := range s.Config.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
func (s *Server) publicIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	// Require the trusted edge proxy to overwrite, not append X-Forwarded-For.
	if s.trusted(r) {
		if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Forwarded-For"))); err == nil {
			return ip.String()
		}
	}
	return host
}
func (s *Server) allow(r *http.Request, scope string) bool {
	key := scope + ":" + s.publicIP(r)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.limits[key]
	if b == nil {
		for k, v := range s.limits {
			if now.Sub(v.seen) > 10*time.Minute {
				delete(s.limits, k)
			}
		}
		if len(s.limits) >= 10000 {
			return false
		}
		b = &bucket{limiter: rate.NewLimiter(rate.Every(10*time.Second), 10)}
		s.limits[key] = b
	}
	b.seen = now
	return b.limiter.Allow()
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r, "register") {
		fail(w, 429, "too many requests")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 80 {
		fail(w, 400, "name must be 1-80 bytes")
		return
	}
	out, err := s.Store.Register(r.Context(), body.Name)
	if errors.Is(err, store.ErrConflict) {
		fail(w, 409, "name already registered")
		return
	}
	if err != nil {
		fail(w, 500, "registration failed")
		return
	}
	respond(w, 201, out)
}
func WebSocketURL(base, path string) string {
	u, _ := url.Parse(base)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path
	return u.String()
}
