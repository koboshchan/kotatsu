package store

import (
	"context"
)

func (s *Store) StartSession(ctx context.Context, id, owner, machine string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO sessions(id,owner_id,machine_id,started_at) VALUES(?,?,?,?)", id, owner, machine, s.Now().Unix())
	return err
}
func (s *Store) EndSession(ctx context.Context, id, reason string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE sessions SET ended_at=?,reason=? WHERE id=? AND ended_at IS NULL", s.Now().Unix(), reason, id)
	return err
}
