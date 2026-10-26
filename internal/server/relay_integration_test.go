//go:build linux

package server_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/koboshchan/kotatsu/internal/agent"
	"github.com/koboshchan/kotatsu/internal/api"
	"github.com/koboshchan/kotatsu/internal/client"
	"github.com/koboshchan/kotatsu/internal/config"
	"github.com/koboshchan/kotatsu/internal/relay"
	"github.com/koboshchan/kotatsu/internal/server"
	"github.com/koboshchan/kotatsu/internal/store"
	"github.com/koboshchan/kotatsu/internal/store/storetest"
	"golang.org/x/term"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
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
	db, e := storetest.Open(t), error(nil)
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
	token, _ := db.CreateEnrollment(ctx, a.Account.ID, false, time.Minute, false)
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

func TestPipedEOFAndMachineRevocation(t *testing.T) {
	f := newFixture(t)
	probe := f.dial(t, f.alice.Token.Token)
	probe.CloseNow()
	cli, _ := client.New(f.http.URL, f.alice.Token.Token, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if e := cli.Connect(ctx, f.machine.Machine.DisplayID, strings.NewReader("printf 'EOF_%s\\n' OK\n"), &output); e != nil {
		t.Fatal(e, output.String())
	}
	if !strings.Contains(output.String(), "EOF_OK") {
		t.Fatal(output.String())
	}
	c := f.dial(t, f.alice.Token.Token)
	defer c.CloseNow()
	session := ready(t, c)
	bob, _ := client.New(f.http.URL, f.bob.Token.Token, true)
	if e := bob.Do(session, "DELETE", api.Prefix+"/machines/"+f.machine.Machine.DisplayID, nil, nil); e == nil {
		t.Fatal("IDOR machine revoke")
	}
	if e := cli.Do(session, "DELETE", api.Prefix+"/machines/"+f.machine.Machine.DisplayID, nil, nil); e != nil {
		t.Fatal(e)
	}
	for {
		_, e := relay.Read(session, c)
		if e != nil {
			break
		}
	}
	if session.Err() != nil {
		t.Fatal("machine revocation did not close session")
	}
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer verifyCancel()
	if _, e := f.db.MachineAuth(verifyCtx, f.machine.Credential); e != store.ErrUnauthorized {
		t.Fatal(e)
	}
	if c, e := relay.Dial(verifyCtx, f.http.URL, api.Prefix+"/agent/connect", f.machine.Credential); e == nil {
		c.CloseNow()
		t.Fatal("revoked machine reconnect")
	}
}

func TestDetachedProcessKilledOnDisconnect(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t, f.alice.Token.Token)
	ctx := ready(t, c)
	file := filepath.Join(t.TempDir(), "detached.pid")
	// setsid is a common intentional detachment that process-group-only cleanup
	// misses. The per-session subreaper must still reap this process.
	command := fmt.Sprintf("setsid /bin/sh -c 'echo $$ > %s; exec sleep 60' >/dev/null 2>&1 < /dev/null &\n", file)
	_ = relay.Write(ctx, c, relay.Frame{Type: "data", Data: []byte(command)})
	deadline := time.After(3 * time.Second)
	var pid int
	for pid == 0 {
		b, _ := os.ReadFile(file)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if pid == 0 {
			select {
			case <-deadline:
				t.Fatal("detached process did not start")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	_ = c.CloseNow()
	deadline = time.After(4 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("detached process %d survived disconnect", pid)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestIdleAndMaximumSessionExpiry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		idle, max time.Duration
	}{{"idle", 80 * time.Millisecond, time.Hour}, {"maximum", time.Hour, 80 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.app.Config.SessionIdle = tc.idle
			f.app.Config.SessionMax = tc.max
			c := f.dial(t, f.alice.Token.Token)
			defer c.CloseNow()
			ctx := ready(t, c)
			for {
				_, e := relay.Read(ctx, c)
				if e != nil {
					break
				}
			}
			if ctx.Err() != nil {
				t.Fatal("session expiry not enforced")
			}
		})
	}
}

func TestCLI_SIGTERMRestoresTerminal(t *testing.T) {
	f := newFixture(t)
	probe := f.dial(t, f.alice.Token.Token)
	probe.CloseNow()
	binary := filepath.Join(t.TempDir(), "kotatsu")
	build := exec.Command("go", "build", "-o", binary, "./cmd/kotatsu")
	build.Dir = "../.."
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	cfgPath := filepath.Join(t.TempDir(), "config")
	if e := config.PrivateJSON(cfgPath, config.Client{Server: f.http.URL, Token: f.alice.Token.Token, Dev: true}); e != nil {
		t.Fatal(e)
	}
	master, slave, e := pty.Open()
	if e != nil {
		t.Fatal(e)
	}
	defer master.Close()
	defer slave.Close()
	original, e := term.GetState(int(slave.Fd()))
	if e != nil {
		t.Fatal(e)
	}
	command := exec.Command(binary, "--config", cfgPath, "connect", f.machine.Machine.DisplayID)
	command.Stdin = slave
	command.Stdout = slave
	command.Stderr = slave
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	defer command.Process.Kill()
	rawDeadline := time.After(4 * time.Second)
	for {
		current, e := term.GetState(int(slave.Fd()))
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(current, original) {
			break
		}
		select {
		case <-rawDeadline:
			t.Fatal("CLI did not enter raw mode")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if e = command.Process.Signal(syscall.SIGTERM); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CLI did not stop on SIGTERM")
	}
	restored, e := term.GetState(int(slave.Fd()))
	if e != nil || !reflect.DeepEqual(restored, original) {
		t.Fatal("terminal was not restored", e)
	}
}
