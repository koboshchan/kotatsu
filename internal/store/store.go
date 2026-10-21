package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/auth"
	"math/big"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

var ErrUnauthorized = errors.New("invalid or revoked credential")
var ErrConflict = errors.New("name already registered")
var ErrNotFound = errors.New("not found")

type Identity struct {
	Account api.Account
	TokenID string
}
type Store struct {
	db           *sql.DB
	Now          func() time.Time
	NewDisplayID func() (string, error)
}

const migration = `
CREATE TABLE IF NOT EXISTS schema_version(version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE CHECK(length(name) BETWEEN 1 AND 80), created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS account_tokens(id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES accounts(id), hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)));
CREATE INDEX IF NOT EXISTS account_tokens_owner ON account_tokens(owner_id);
CREATE TABLE IF NOT EXISTS enrollment_tokens(id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES accounts(id), hash TEXT NOT NULL UNIQUE, reusable INTEGER NOT NULL CHECK(reusable IN (0,1)), expires_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)), used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS machines(id TEXT PRIMARY KEY, display_id TEXT NOT NULL UNIQUE CHECK(length(display_id)=7 AND display_id NOT GLOB '*[^0-9]*'), owner_id TEXT NOT NULL REFERENCES accounts(id), name TEXT NOT NULL, platform TEXT NOT NULL, public_ip TEXT NOT NULL, created_at INTEGER NOT NULL, last_seen INTEGER NOT NULL, credential_hash TEXT NOT NULL UNIQUE);
CREATE INDEX IF NOT EXISTS machines_owner ON machines(owner_id);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES accounts(id), machine_id TEXT NOT NULL REFERENCES machines(id), started_at INTEGER NOT NULL, ended_at INTEGER, reason TEXT);
INSERT OR IGNORE INTO schema_version(version) VALUES(1);
`

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		f.Close()
		if err = os.Chmod(path, 0600); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, Now: time.Now, NewDisplayID: randomDisplayID}
	for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", migration} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	// Version two adds permanent owner-triggered machine credential revocation.
	var hasRevocation int
	if err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('machines') WHERE name='revoked'").Scan(&hasRevocation); err != nil {
		db.Close()
		return nil, err
	}
	if hasRevocation == 0 {
		if _, err = db.Exec("ALTER TABLE machines ADD COLUMN revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1))"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err = db.Exec("INSERT OR IGNORE INTO schema_version(version) VALUES(2)"); err != nil {
		db.Close()
		return nil, err
	}
	// Relays are deliberately transient; after a restart no historical session
	// remains marked active.
	_, err = db.Exec("UPDATE sessions SET ended_at=?, reason='server_restart' WHERE ended_at IS NULL", s.Now().Unix())
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func randomDisplayID() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(9000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%07d", n.Int64()+1000000), nil
}
func (s *Store) Register(ctx context.Context, name string) (api.Registration, error) {
	var out api.Registration
	if len(name) < 1 || len(name) > 80 {
		return out, errors.New("name must be 1-80 bytes")
	}
	raw, err := auth.New("as")
	if err != nil {
		return out, err
	}
	out.Account = api.Account{ID: uuid.NewString(), Name: name}
	out.Token = api.Token{ID: uuid.NewString(), Token: raw, CreatedAt: s.Now().UTC()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO accounts VALUES(?,?,?)", out.Account.ID, name, s.Now().Unix())
	if err != nil {
		return api.Registration{}, ErrConflict
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO account_tokens(id,owner_id,hash,created_at) VALUES(?,?,?,?)", out.Token.ID, out.Account.ID, auth.Hash(raw), out.Token.CreatedAt.Unix())
	if err != nil {
		return api.Registration{}, err
	}
	err = tx.Commit()
	return out, err
}
func (s *Store) Authenticate(ctx context.Context, token string) (Identity, error) {
	var i Identity
	err := s.db.QueryRowContext(ctx, "SELECT a.id,a.name,t.id FROM account_tokens t JOIN accounts a ON a.id=t.owner_id WHERE t.hash=? AND t.revoked=0", auth.Hash(token)).Scan(&i.Account.ID, &i.Account.Name, &i.TokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrUnauthorized
	}
	return i, err
}
func (s *Store) Tokens(ctx context.Context, owner string) ([]api.Token, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,created_at FROM account_tokens WHERE owner_id=? AND revoked=0 ORDER BY created_at,id", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Token{}
	for rows.Next() {
		var t api.Token
		var n int64
		if err = rows.Scan(&t.ID, &n); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(n, 0).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) IssueToken(ctx context.Context, owner, rotateID string) (api.Token, error) {
	raw, err := auth.New("as")
	if err != nil {
		return api.Token{}, err
	}
	out := api.Token{ID: uuid.NewString(), Token: raw, CreatedAt: s.Now().UTC()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Token{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO account_tokens(id,owner_id,hash,created_at) VALUES(?,?,?,?)", out.ID, owner, auth.Hash(raw), out.CreatedAt.Unix())
	if err != nil {
		return api.Token{}, err
	}
	if rotateID != "" {
		r, e := tx.ExecContext(ctx, "UPDATE account_tokens SET revoked=1 WHERE owner_id=? AND id=? AND revoked=0", owner, rotateID)
		if e != nil {
			return api.Token{}, e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return api.Token{}, ErrNotFound
		}
	}
	err = tx.Commit()
	return out, err
}
func (s *Store) RevokeToken(ctx context.Context, owner, id string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE account_tokens SET revoked=1 WHERE owner_id=? AND id=? AND revoked=0", owner, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
