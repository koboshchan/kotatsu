package store

import (
	"context"
	"github.com/koboshchan/anyshell/internal/auth"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestTokenLifecycle(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a, e := s.Register(ctx, "alice")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := s.Register(ctx, "bob")
	if _, e = s.Register(ctx, "alice"); e != ErrConflict {
		t.Fatal(e)
	}
	i, e := s.Authenticate(ctx, a.Token.Token)
	if e != nil || i.Account.ID != a.Account.ID {
		t.Fatal(i, e)
	}
	if e = s.RevokeToken(ctx, b.Account.ID, a.Token.ID); e != ErrNotFound {
		t.Fatal("IDOR", e)
	}
	next, e := s.IssueToken(ctx, a.Account.ID, a.Token.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, a.Token.Token); e != ErrUnauthorized {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, next.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeToken(ctx, a.Account.ID, next.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, next.Token); e != ErrUnauthorized {
		t.Fatal(e)
	}
	rows, e := s.db.Query("SELECT hash FROM account_tokens")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		rows.Scan(&hash)
		if len(hash) != 64 || strings.HasPrefix(hash, "as_") {
			t.Fatal("plaintext credential")
		}
	}
}
func TestHash(t *testing.T) {
	a, _ := auth.New("as")
	b, _ := auth.New("as")
	if a == b || auth.Hash(a) == auth.Hash(b) || auth.Hash(a) != auth.Hash(a) {
		t.Fatal("random/hash")
	}
	if auth.Bearer("Bearer "+a, "as") != a || auth.Bearer("Bearer "+a, "am") != "" {
		t.Fatal("kind")
	}
}
func TestReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db")
	s, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := s.Register(context.Background(), "persistent")
	s.Close()
	s, e = Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Authenticate(context.Background(), a.Token.Token); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}
