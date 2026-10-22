package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/koboshchan/kotatsu/internal/auth"
	"go.mongodb.org/mongo-driver/v2/bson"
	"os"
	"strings"
	"testing"
	"time"
)

func testURI(t *testing.T) string {
	t.Helper()
	u := os.Getenv("KOTATSU_TEST_MONGO_URI")
	if u == "" {
		t.Skip("set KOTATSU_TEST_MONGO_URI to a disposable MongoDB")
	}
	return u
}
func testDB() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "kotatsu_test_" + hex.EncodeToString(b)
}

func setup(t *testing.T) *Store {
	t.Helper()
	s, e := Open(testURI(t), testDB())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.DropDatabase(ctx)
		s.Close()
	})
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
	cur, e := s.c("account_tokens").Find(ctx, bson.M{})
	if e != nil {
		t.Fatal(e)
	}
	var docs []struct {
		Hash string `bson:"hash"`
	}
	if e = cur.All(ctx, &docs); e != nil || len(docs) != 2 {
		t.Fatal(e, len(docs))
	}
	for _, d := range docs {
		if len(d.Hash) != 64 || strings.HasPrefix(d.Hash, "as_") {
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
	uri, name := testURI(t), testDB()
	s, e := Open(uri, name)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := s.Register(context.Background(), "persistent")
	s.Close()
	s, e = Open(uri, name)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.DropDatabase(context.Background()); s.Close() }()
	if _, e = s.Authenticate(context.Background(), a.Token.Token); e != nil {
		t.Fatal(e)
	}
}
