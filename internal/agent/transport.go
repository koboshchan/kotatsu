package agent

import (
	"context"
	"errors"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/relay"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"time"
)

func Run(ctx context.Context, st State) error {
	if os.Geteuid() == 0 {
		return errors.New("agent must not run as root")
	}
	if st.Credential == "" {
		return errors.New("missing machine identity")
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		started := time.Now()
		err := serveConnection(ctx, st)
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("agent disconnected; retrying", "error", err, "retry_in", backoff)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		timer := time.NewTimer(backoff + time.Duration(rand.Int64N(int64(backoff/4+1))))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}
func serveConnection(ctx context.Context, st State) error {
	c, err := relay.Dial(ctx, st.Server, api.Prefix+"/agent/connect", st.Credential)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	connection, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	sessions := map[string]*shellSession{}
	var wg sync.WaitGroup
	defer func() {
		cancel()
		mu.Lock()
		for _, v := range sessions {
			v.cancel()
		}
		mu.Unlock()
		wg.Wait()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			if err := relay.Write(connection, c, relay.Frame{Type: "heartbeat"}); err != nil {
				cancel()
				_ = c.CloseNow()
				return
			}
			select {
			case <-connection.Done():
				_ = c.CloseNow()
				return
			case <-ticker.C:
			}
		}
	}()
	for {
		f, e := relay.Read(connection, c)
		if e != nil {
			return e
		}
		if f.Session == "" {
			return errors.New("missing session ID")
		}
		mu.Lock()
		session := sessions[f.Session]
		mu.Unlock()
		switch f.Type {
		case "open":
			if session != nil {
				return errors.New("duplicate session")
			}
			if !f.ValidSize() || !f.ValidTerm() {
				return errors.New("invalid terminal settings")
			}
			mu.Lock()
			full := len(sessions) >= 4
			mu.Unlock()
			if full {
				_ = relay.Write(connection, c, relay.Frame{Type: "exit", Session: f.Session, Code: 1, Reason: "session limit"})
				continue
			}
			session, e = startShell(connection, c, f)
			if e != nil {
				_ = relay.Write(connection, c, relay.Frame{Type: "exit", Session: f.Session, Code: 1, Reason: "shell unavailable"})
				continue
			}
			mu.Lock()
			sessions[f.Session] = session
			mu.Unlock()
			wg.Add(1)
			go func(id string, p *shellSession) {
				defer wg.Done()
				p.run()
				mu.Lock()
				delete(sessions, id)
				mu.Unlock()
			}(f.Session, session)
		case "data", "eof":
			if session == nil {
				continue
			}
			if f.Type == "eof" {
				f.Data = []byte{4, 4}
			}
			select {
			case session.input <- f.Data:
			case <-session.ctx.Done():
			default:
				session.cancel()
			}
		case "resize":
			if session == nil {
				continue
			}
			if !f.ValidSize() {
				return errors.New("invalid resize")
			}
			if e = session.resize(f.Rows, f.Cols); e != nil {
				session.cancel()
			}
		case "close":
			if session != nil {
				session.cancel()
			}
		default:
			return errors.New("unexpected relay message")
		}
	}
}
