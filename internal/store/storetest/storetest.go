// Package storetest opens an isolated MongoDB database for tests.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/koboshchan/kotatsu/internal/store"
)

// Open connects to KOTATSU_TEST_MONGO_URI and uses a unique database that is
// dropped on cleanup. Tests are skipped when no test MongoDB is configured.
func Open(t testing.TB) *store.Store {
	t.Helper()
	uri := os.Getenv("KOTATSU_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set KOTATSU_TEST_MONGO_URI to a disposable MongoDB")
	}
	b := make([]byte, 8)
	rand.Read(b)
	s, err := store.Open(uri, "kotatsu_test_"+hex.EncodeToString(b))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.DropDatabase(ctx)
		s.Close()
	})
	return s
}
