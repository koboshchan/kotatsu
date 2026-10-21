//go:build linux

package server_test

import (
	"bytes"
	"context"
	"github.com/coder/websocket"
	"github.com/koboshchan/anyshell/internal/agent"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/client"
	"github.com/koboshchan/anyshell/internal/relay"
	"github.com/koboshchan/anyshell/internal/server"
	"github.com/koboshchan/anyshell/internal/store"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	db         *store.Store
	app        *server.Server
	http       *httptest.Server
	alice, bob api.Registration
	machine    api.Enrolled
	cancel     context.CancelFunc
	done       chan error
	state      agent.State
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	app, e := server.New(db, server.Config{PublicURL: "http://localhost", Dev: true, SessionCheck: 20 * time.Millisecond}, nil)
	if e != nil {
		t.Fatal(e)
	}
	http := httptest.NewServer(app)
	ctx := context.Background()
	a, _ := db.Register(ctx, "alice")
	b, _ := db.Register(ctx, "bob")
	f := &fixture{db: db, app: app, http: http, alice: a, bob: b}
	token, _ := db.CreateEnrollment(ctx, a.Account.ID, false, time.Minute)
	state, e := agent.Enroll(ctx, http.URL, token.Token, filepath.Join(t.TempDir(), "agent.json"), true)
	if e != nil {
		t.Fatal(e)
	}
	f.state = state
	f.machine = api.Enrolled{Machine: state.Machine, Credential: state.Credential}
	f.start()
	t.Cleanup(func() {
		f.cancel()
		select {
		case <-f.done:
		case <-time.After(5 * time.Second):
			t.Error("agent stop timeout")
		}
		if e := app.Close(); e != nil {
			t.Error(e)
		}
		http.Close()
		db.Close()
	})
	return f
}
func (f *fixture) start() {
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan error, 1)
	go func() { f.done <- agent.Run(ctx, f.state) }()
}
func (f *fixture) dial(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		c, e := relay.Dial(ctx, f.http.URL, api.Prefix+"/machines/"+f.machine.Machine.DisplayID+"/connect", token)
		if e == nil {
			return c
		}
		select {
		case <-ctx.Done():
			t.Fatal(e)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func ready(t *testing.T, c *websocket.Conn) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if e := relay.Write(ctx, c, relay.Frame{Type: "start", Rows: 31, Cols: 93, Term: "xterm"}); e != nil {
		t.Fatal(e)
	}
	frame, e := relay.Read(ctx, c)
	if e != nil || frame.Type != "ready" {
		t.Fatal(frame, e)
	}
	return ctx
}
func outputUntil(t *testing.T, ctx context.Context, c *websocket.Conn, needle string) string {
	t.Helper()
	var out strings.Builder
	for {
		f, e := relay.Read(ctx, c)
		if e != nil {
			t.Fatal(e, out.String())
		}
		if f.Type == "data" {
			out.Write(f.Data)
			if strings.Contains(out.String(), needle) {
				return out.String()
			}
		} else if f.Type == "exit" {
			t.Fatal("premature exit", out.String())
		}
	}
}
func TestPTYResizeExitAndIsolation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if c, e := relay.Dial(ctx, f.http.URL, api.Prefix+"/machines/"+f.machine.Machine.DisplayID+"/connect", f.bob.Token.Token); e == nil {
		c.CloseNow()
		t.Fatal("cross account connect")
	}
	if c, e := relay.Dial(ctx, f.http.URL, api.Prefix+"/agent/connect", f.alice.Token.Token); e == nil {
		c.CloseNow()
		t.Fatal("account credential used as machine")
	}
	c := f.dial(t, f.alice.Token.Token)
	defer c.CloseNow()
	session := ready(t, c)
	if e := relay.Write(session, c, relay.Frame{Type: "data", Data: []byte("stty size\n")}); e != nil {
		t.Fatal(e)
	}
	outputUntil(t, session, c, "31 93")
	if e := relay.Write(session, c, relay.Frame{Type: "resize", Rows: 47, Cols: 122}); e != nil {
		t.Fatal(e)
	}
	_ = relay.Write(session, c, relay.Frame{Type: "data", Data: []byte("stty size\n")})
	outputUntil(t, session, c, "47 122")
	_ = relay.Write(session, c, relay.Frame{Type: "data", Data: []byte("printf 'PTY_%s\\n' OK; exit 7\n")})
	var out strings.Builder
	gotExit := false
	for {
		frame, e := relay.Read(session, c)
		if e != nil {
			break
		}
		if frame.Type == "data" {
			out.Write(frame.Data)
		}
		if frame.Type == "exit" {
			if frame.Code != 7 {
				t.Fatal(frame)
			}
			gotExit = true
			break
		}
	}
	if !gotExit || !strings.Contains(out.String(), "PTY_OK") {
		t.Fatal(gotExit, out.String())
	}
	list, _ := f.db.Machines(ctx, f.bob.Account.ID)
	if len(list) != 0 {
		t.Fatal("cross account inventory")
	}
}
func TestAgentRestartAndCLIConnect(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, f.alice.Token.Token)
	c.CloseNow()
	id := f.state.Machine.ID
	f.cancel()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
	}
	f.start()
	c = f.dial(t, f.alice.Token.Token)
	c.CloseNow()
	list, _ := f.db.Machines(context.Background(), f.alice.Account.ID)
	if len(list) != 1 || list[0].ID != id {
		t.Fatal(list)
	}
	cli, _ := client.New(f.http.URL, f.alice.Token.Token, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output bytes.Buffer
	if e := cli.Connect(ctx, f.machine.Machine.DisplayID, strings.NewReader("printf 'CLI_%s\\n' OK\nexit\n"), &output); e != nil {
		t.Fatal(e, output.String())
	}
	if !strings.Contains(output.String(), "CLI_OK") {
		t.Fatal(output.String())
	}
}
func TestRevokeDisconnectAndShutdown(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, f.alice.Token.Token)
	defer c.CloseNow()
	ctx := ready(t, c)
	if e := f.db.RevokeToken(ctx, f.alice.Account.ID, f.alice.Token.ID); e != nil {
		t.Fatal(e)
	}
	for {
		_, e := relay.Read(ctx, c)
		if e != nil {
			break
		}
	}
	if ctx.Err() != nil {
		t.Fatal("revocation did not terminate session")
	}
	if e := f.app.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestClientCannotChooseSessionOrCommand(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, f.alice.Token.Token)
	defer c.CloseNow()
	ctx := ready(t, c)
	_ = relay.Write(ctx, c, relay.Frame{Type: "open", Session: "arbitrary", Data: []byte("id")})
	for {
		_, e := relay.Read(ctx, c)
		if e != nil {
			break
		}
	}
	if ctx.Err() != nil {
		t.Fatal("invalid frame not rejected")
	}
}
