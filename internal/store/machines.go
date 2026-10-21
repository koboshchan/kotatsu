package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/auth"
	"time"
)

func (s *Store) CreateEnrollment(ctx context.Context, owner string, reusable bool, ttl time.Duration) (api.Enrollment, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return api.Enrollment{}, errors.New("expiry must be within 24 hours")
	}
	raw, err := auth.New("ae")
	if err != nil {
		return api.Enrollment{}, err
	}
	out := api.Enrollment{ID: uuid.NewString(), Token: raw, Reusable: reusable, ExpiresAt: s.Now().Add(ttl).UTC()}
	_, err = s.db.ExecContext(ctx, "INSERT INTO enrollment_tokens(id,owner_id,hash,reusable,expires_at) VALUES(?,?,?,?,?)", out.ID, owner, auth.Hash(raw), reusable, out.ExpiresAt.Unix())
	return out, err
}
func (s *Store) Enrollments(ctx context.Context, owner string) ([]api.Enrollment, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,reusable,expires_at,used,revoked FROM enrollment_tokens WHERE owner_id=? ORDER BY expires_at DESC", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Enrollment{}
	for rows.Next() {
		var e api.Enrollment
		var expires int64
		if err = rows.Scan(&e.ID, &e.Reusable, &expires, &e.Used, &e.Revoked); err != nil {
			return nil, err
		}
		e.ExpiresAt = time.Unix(expires, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) RevokeEnrollment(ctx context.Context, owner, id string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE enrollment_tokens SET revoked=1 WHERE owner_id=? AND id=?", owner, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) EnrollmentValid(ctx context.Context, token string) error {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM enrollment_tokens WHERE hash=? AND revoked=0 AND expires_at>? AND (reusable=1 OR used=0)", auth.Hash(token), s.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	return err
}
func (s *Store) Enroll(ctx context.Context, in api.EnrollRequest, ip string) (api.Enrolled, error) {
	out := api.Enrolled{}
	if len(in.Name) < 1 || len(in.Name) > 255 || len(in.Platform) > 80 {
		return out, errors.New("invalid machine metadata")
	}
	credential, err := auth.New("am")
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var owner, id string
	err = tx.QueryRowContext(ctx, "SELECT owner_id,id FROM enrollment_tokens WHERE hash=? AND revoked=0 AND expires_at>? AND (reusable=1 OR used=0)", auth.Hash(in.Token), s.Now().Unix()).Scan(&owner, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrUnauthorized
	}
	if err != nil {
		return out, err
	}
	r, err := tx.ExecContext(ctx, "UPDATE enrollment_tokens SET used=used+1 WHERE id=? AND (reusable=1 OR used=0) AND revoked=0 AND expires_at>?", id, s.Now().Unix())
	if err != nil {
		return out, err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return out, ErrUnauthorized
	}
	m := api.Machine{ID: uuid.NewString(), Name: in.Name, Platform: in.Platform, PublicIP: ip, CreatedAt: s.Now().UTC(), LastSeen: s.Now().UTC(), Online: true}
	for tries := 0; tries < 32; tries++ {
		m.DisplayID, err = s.NewDisplayID()
		if err != nil {
			return out, err
		}
		var exists int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM machines WHERE display_id=?", m.DisplayID).Scan(&exists)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO machines(id,display_id,owner_id,name,platform,public_ip,created_at,last_seen,credential_hash) VALUES(?,?,?,?,?,?,?,?,?)", m.ID, m.DisplayID, owner, m.Name, m.Platform, ip, m.CreatedAt.Unix(), m.LastSeen.Unix(), auth.Hash(credential))
		if err != nil {
			return out, err
		}
		if err = tx.Commit(); err != nil {
			return out, err
		}
		return api.Enrolled{Machine: m, Credential: credential}, nil
	}
	return out, fmt.Errorf("machine ID allocation exhausted")
}
func scanMachine(row interface{ Scan(...any) error }, now time.Time) (api.Machine, error) {
	var m api.Machine
	var created, seen int64
	err := row.Scan(&m.ID, &m.DisplayID, &m.Name, &m.Platform, &m.PublicIP, &created, &seen, &m.Revoked)
	m.CreatedAt = time.Unix(created, 0).UTC()
	m.LastSeen = time.Unix(seen, 0).UTC()
	m.Online = !m.Revoked && now.Sub(m.LastSeen) < 90*time.Second
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

const machineColumns = "id,display_id,name,platform,public_ip,created_at,last_seen,revoked"

func (s *Store) Machines(ctx context.Context, owner string) ([]api.Machine, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+machineColumns+" FROM machines WHERE owner_id=? ORDER BY created_at,display_id", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Machine{}
	for rows.Next() {
		m, e := scanMachine(rows, s.Now())
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) OwnedMachine(ctx context.Context, owner, displayID string) (api.Machine, error) {
	return scanMachine(s.db.QueryRowContext(ctx, "SELECT "+machineColumns+" FROM machines WHERE owner_id=? AND display_id=?", owner, displayID), s.Now())
}
func (s *Store) MachineAuth(ctx context.Context, credential string) (api.Machine, error) {
	m, err := scanMachine(s.db.QueryRowContext(ctx, "SELECT "+machineColumns+" FROM machines WHERE credential_hash=? AND revoked=0", auth.Hash(credential)), s.Now())
	if errors.Is(err, ErrNotFound) {
		return m, ErrUnauthorized
	}
	return m, err
}
func (s *Store) Heartbeat(ctx context.Context, machineID, ip string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE machines SET last_seen=?,public_ip=? WHERE id=? AND revoked=0", s.Now().Unix(), ip, machineID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RevokeMachine(ctx context.Context, owner, displayID string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE machines SET revoked=1 WHERE owner_id=? AND display_id=?", owner, displayID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
