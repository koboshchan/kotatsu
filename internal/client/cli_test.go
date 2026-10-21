package client

import (
	"bytes"
	"context"
	"github.com/koboshchan/anyshell/internal/config"
	"github.com/koboshchan/anyshell/internal/server"
	"github.com/koboshchan/anyshell/internal/store"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIAccountAndInventory(t *testing.T) {
	db, e := store.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	app, _ := server.New(db, server.Config{PublicURL: "http://localhost", Dev: true}, nil)
	srv := httptest.NewServer(app)
	defer srv.Close()
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	invoke := func(input string, args ...string) (string, error) {
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetIn(strings.NewReader(input))
		cmd.SetArgs(append([]string{"--config", cfgPath}, args...))
		e := cmd.ExecuteContext(context.Background())
		return out.String(), e
	}
	out, e := invoke("", "--server", srv.URL, "--dev", "auth", "register", "--name", "alice")
	if e != nil || !strings.Contains(out, "shown once") {
		t.Fatal(out, e)
	}
	var cfg config.Client
	if e = config.Load(cfgPath, &cfg); e != nil {
		t.Fatal(e)
	}
	if _, e = invoke("", "auth", "whoami"); e != nil {
		t.Fatal(e)
	}
	out, e = invoke("", "ls", "--json")
	if e != nil || strings.TrimSpace(out) != "[]" {
		t.Fatal(out, e)
	}
	out, e = invoke("", "enrollment", "create")
	if e != nil || !strings.Contains(out, "/script/ae_") {
		t.Fatal(out, e)
	}
	if _, e = invoke("", "auth", "rotate"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Authenticate(context.Background(), cfg.Token); e != store.ErrUnauthorized {
		t.Fatal("rotation")
	}
	if _, e = invoke("", "auth", "logout"); e != nil {
		t.Fatal(e)
	}
	if _, e = invoke("", "ls"); e == nil {
		t.Fatal("logout")
	}
	if _, e = invoke(cfg.Token+"\n", "auth", "login"); e == nil {
		t.Fatal("revoked login")
	}
	config.Load(cfgPath, &cfg)
	if cfg.Token != "" {
		t.Fatal("stored failed token")
	}
}
