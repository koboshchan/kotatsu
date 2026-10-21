package server

import (
	"context"
	"errors"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/auth"
	"github.com/koboshchan/anyshell/internal/relay"
	"net/http"
	"sync"
	"time"
)

type agentLink struct {
	conn    *websocket.Conn
	machine string
}
type liveSession struct {
	id      string
	owner   string
	token   string
	machine string
	agent   *agentLink
	frames  chan relay.Frame
	done    chan struct{}
	cancel  context.CancelFunc
	mu      sync.Mutex
	last    time.Time
}

func (s *liveSession) touch() { s.mu.Lock(); s.last = time.Now(); s.mu.Unlock() }
func (s *liveSession) idle() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.last)
}
func (s *Server) relayRoutes() {
	s.mux.HandleFunc("GET "+api.Prefix+"/agent/connect", s.agentSocket)
	s.handle("GET "+api.Prefix+"/machines/{id}/connect", s.cliSocket)
}
func (s *Server) agentSocket(w http.ResponseWriter, r *http.Request) {
	token := auth.Bearer(r.Header.Get("Authorization"), "am")
	if token == "" {
		fail(w, 401, "machine credential required")
		return
	}
	m, err := s.Store.MachineAuth(r.Context(), token)
	if err != nil {
		fail(w, 401, "invalid machine credential")
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(relay.MaxMessage)
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	link := &agentLink{conn: c, machine: m.ID}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	old := s.agents[m.ID]
	s.agents[m.ID] = link
	s.mu.Unlock()
	if old != nil {
		_ = old.conn.CloseNow()
	}
	defer func() {
		s.mu.Lock()
		if s.agents[m.ID] == link {
			delete(s.agents, m.ID)
		}
		for _, session := range s.sessions {
			if session.agent == link {
				session.cancel()
			}
		}
		s.mu.Unlock()
	}()
	_ = s.Store.Heartbeat(ctx, m.ID, s.publicIP(r))
	// Closing a silent/stuck control socket ensures no orphan relay survives.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Since(lastSeen(s, link)) > 90*time.Second {
					_ = c.CloseNow()
					return
				}
			}
		}
	}()
	s.mu.Lock()
	s.agentSeen[link] = time.Now()
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.agentSeen, link); s.mu.Unlock() }()
	for {
		f, e := relay.Read(ctx, c)
		if e != nil {
			return
		}
		switch f.Type {
		case "heartbeat":
			if f.Session != "" {
				return
			}
			if e = s.Store.Heartbeat(ctx, m.ID, s.publicIP(r)); e != nil {
				return
			}
			s.mu.Lock()
			s.agentSeen[link] = time.Now()
			s.mu.Unlock()
		case "ready", "data", "exit":
			s.mu.Lock()
			session := s.sessions[f.Session]
			s.mu.Unlock()
			if session == nil {
				continue
			}
			if session.agent != link {
				return
			}
			if f.Type == "data" {
				session.touch()
			}
			select {
			case session.frames <- f:
			case <-session.done:
			case <-ctx.Done():
				return
			default:
				session.cancel()
			}
		default:
			return
		}
	}
}
func lastSeen(s *Server, link *agentLink) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentSeen[link]
}
func (s *Server) cliSocket(w http.ResponseWriter, r *http.Request) {
	i := identity(r)
	m, err := s.Store.OwnedMachine(r.Context(), i.Account.ID, r.PathValue("id"))
	if err != nil {
		fail(w, 404, "machine not found")
		return
	}
	if !s.allow(r, "connect") {
		fail(w, 429, "too many session requests")
		return
	}
	s.mu.Lock()
	link := s.agents[m.ID]
	count := 0
	for _, v := range s.sessions {
		if v.machine == m.ID {
			count++
		}
	}
	closing := s.closing
	s.mu.Unlock()
	if closing || link == nil || !m.Online {
		fail(w, 409, "machine offline")
		return
	}
	if count >= 4 {
		fail(w, 429, "machine session limit reached")
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(relay.MaxMessage)
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(r.Context(), s.maxDuration())
	defer cancel()
	startCtx, startCancel := context.WithTimeout(ctx, 10*time.Second)
	start, err := relay.Read(startCtx, c)
	startCancel()
	if err != nil || start.Type != "start" || start.Session != "" || !start.ValidSize() || !start.ValidTerm() {
		_ = c.Close(websocket.StatusPolicyViolation, "invalid start frame")
		return
	}
	session := &liveSession{id: uuid.NewString(), owner: i.Account.ID, token: auth.Bearer(r.Header.Get("Authorization"), "as"), machine: m.ID, agent: link, frames: make(chan relay.Frame, 32), done: make(chan struct{}), cancel: cancel, last: time.Now()}
	s.mu.Lock()
	count = 0
	for _, v := range s.sessions {
		if v.machine == m.ID {
			count++
		}
	}
	if s.closing || s.agents[m.ID] != link || count >= 4 {
		s.mu.Unlock()
		return
	}
	s.sessions[session.id] = session
	s.mu.Unlock()
	reason := "disconnected"
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.sessions, session.id)
		close(session.done)
		s.mu.Unlock()
		closeCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = relay.Write(closeCtx, link.conn, relay.Frame{Type: "close", Session: session.id})
		_ = s.Store.EndSession(closeCtx, session.id, reason)
		s.Log.Info("session ended", "session", session.id, "reason", reason)
	}()
	if err = s.Store.StartSession(ctx, session.id, i.Account.ID, m.ID); err != nil {
		return
	}
	s.Log.Info("session started", "session", session.id, "machine", m.DisplayID, "account", i.Account.ID)
	start.Type = "open"
	start.Session = session.id
	if err = relay.Write(ctx, link.conn, start); err != nil {
		return
	}
	clientFrames := make(chan relay.Frame, 16)
	readDone := make(chan error, 1)
	go func() {
		for {
			f, e := relay.Read(ctx, c)
			if e != nil {
				readDone <- e
				return
			}
			select {
			case clientFrames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(s.checkInterval())
	defer ticker.Stop()
	ready := false
	for {
		select {
		case <-ctx.Done():
			reason = "session_timeout_or_agent_disconnect"
			return
		case <-readDone:
			return
		case <-ticker.C:
			if session.idle() > s.idleDuration() {
				reason = "idle_timeout"
				return
			}
			if _, err = s.Store.Authenticate(ctx, session.token); err != nil {
				reason = "token_revoked"
				return
			}
		case f := <-session.frames:
			if f.Type == "ready" {
				if ready {
					return
				}
				ready = true
			}
			f.Session = ""
			if err = relay.Write(ctx, c, f); err != nil {
				return
			}
			if f.Type == "exit" {
				reason = "shell_exit"
				return
			}
		case f := <-clientFrames:
			if !ready {
				reason = "input_before_ready"
				return
			}
			if f.Session != "" {
				reason = "invalid_client_frame"
				return
			}
			switch f.Type {
			case "data":
				session.touch()
			case "resize":
				if !f.ValidSize() {
					return
				}
			case "close":
				return
			default:
				return
			}
			f.Session = session.id
			if err = relay.Write(ctx, link.conn, f); err != nil {
				return
			}
		}
	}
}
func (s *Server) maxDuration() time.Duration {
	if s.Config.SessionMax > 0 {
		return s.Config.SessionMax
	}
	return time.Hour
}
func (s *Server) idleDuration() time.Duration {
	if s.Config.SessionIdle > 0 {
		return s.Config.SessionIdle
	}
	return 10 * time.Minute
}
func (s *Server) checkInterval() time.Duration {
	if s.Config.SessionCheck > 0 {
		return s.Config.SessionCheck
	}
	return time.Second
}
func (s *Server) Close() error {
	s.mu.Lock()
	s.closing = true
	links := make([]*agentLink, 0, len(s.agents))
	for _, link := range s.agents {
		links = append(links, link)
	}
	for _, v := range s.sessions {
		v.cancel()
	}
	s.mu.Unlock()
	for _, link := range links {
		_ = link.conn.CloseNow()
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		n := len(s.sessions)
		s.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return errors.New("timed out draining relays")
		}
	}
}
