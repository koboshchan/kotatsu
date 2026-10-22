package store

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func (s *Store) StartSession(ctx context.Context, id, owner, machine string) error {
	_, err := s.c("sessions").InsertOne(ctx, bson.M{"_id": id, "owner_id": owner, "machine_id": machine, "started_at": s.Now().Unix(), "ended_at": nil, "reason": nil})
	return err
}
func (s *Store) EndSession(ctx context.Context, id, reason string) error {
	_, err := s.c("sessions").UpdateOne(ctx, bson.M{"_id": id, "ended_at": nil}, bson.M{"$set": bson.M{"ended_at": s.Now().Unix(), "reason": reason}})
	return err
}
