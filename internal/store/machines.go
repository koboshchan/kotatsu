package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/koboshchan/kotatsu/internal/api"
	"github.com/koboshchan/kotatsu/internal/auth"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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
	_, err = s.c("enrollment_tokens").InsertOne(ctx, bson.M{"_id": out.ID, "owner_id": owner, "hash": auth.Hash(raw), "reusable": reusable, "expires_at": out.ExpiresAt.Unix(), "revoked": false, "used": 0})
	return out, err
}
func (s *Store) Enrollments(ctx context.Context, owner string) ([]api.Enrollment, error) {
	cur, err := s.c("enrollment_tokens").Find(ctx, bson.M{"owner_id": owner}, options.Find().SetSort(bson.D{{Key: "expires_at", Value: -1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID       string `bson:"_id"`
		Reusable bool   `bson:"reusable"`
		Expires  int64  `bson:"expires_at"`
		Used     int    `bson:"used"`
		Revoked  bool   `bson:"revoked"`
	}
	if err = cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := []api.Enrollment{}
	for _, d := range docs {
		out = append(out, api.Enrollment{ID: d.ID, Reusable: d.Reusable, ExpiresAt: time.Unix(d.Expires, 0).UTC(), Used: d.Used, Revoked: d.Revoked})
	}
	return out, nil
}
func (s *Store) RevokeEnrollment(ctx context.Context, owner, id string) error {
	r, err := s.c("enrollment_tokens").UpdateOne(ctx, bson.M{"owner_id": owner, "_id": id}, bson.M{"$set": bson.M{"revoked": true}})
	if err != nil {
		return err
	}
	if r.MatchedCount != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) enrollmentFilter(token string) bson.M {
	return bson.M{"hash": auth.Hash(token), "revoked": false, "expires_at": bson.M{"$gt": s.Now().Unix()}, "$or": bson.A{bson.M{"reusable": true}, bson.M{"used": 0}}}
}
func (s *Store) EnrollmentValid(ctx context.Context, token string) error {
	err := s.c("enrollment_tokens").FindOne(ctx, s.enrollmentFilter(token)).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
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
	m := api.Machine{ID: uuid.NewString(), Name: in.Name, Platform: in.Platform, PublicIP: ip, CreatedAt: s.Now().UTC(), LastSeen: s.Now().UTC(), Online: true}
	for tries := 0; tries < 32; tries++ {
		m.DisplayID, err = s.NewDisplayID()
		if err != nil {
			return out, err
		}
		err = s.transaction(ctx, func(tx context.Context) error {
			var tok struct {
				Owner string `bson:"owner_id"`
			}
			err := s.c("enrollment_tokens").FindOneAndUpdate(tx, s.enrollmentFilter(in.Token), bson.M{"$inc": bson.M{"used": 1}}).Decode(&tok)
			if errors.Is(err, mongo.ErrNoDocuments) {
				return ErrUnauthorized
			}
			if err != nil {
				return err
			}
			_, err = s.c("machines").InsertOne(tx, bson.M{"_id": m.ID, "display_id": m.DisplayID, "owner_id": tok.Owner, "name": m.Name, "platform": m.Platform, "public_ip": ip, "created_at": m.CreatedAt.Unix(), "last_seen": m.LastSeen.Unix(), "credential_hash": auth.Hash(credential), "revoked": false})
			return err
		})
		// Retry the whole transaction: the rejected ID must not consume a token.
		if mongo.IsDuplicateKeyError(err) {
			continue
		}
		if err != nil {
			return out, err
		}
		return api.Enrolled{Machine: m, Credential: credential}, nil
	}
	return out, fmt.Errorf("machine ID allocation exhausted")
}

type machineDoc struct {
	ID        string `bson:"_id"`
	DisplayID string `bson:"display_id"`
	Name      string `bson:"name"`
	Platform  string `bson:"platform"`
	PublicIP  string `bson:"public_ip"`
	Created   int64  `bson:"created_at"`
	Seen      int64  `bson:"last_seen"`
	Revoked   bool   `bson:"revoked"`
}

func (d machineDoc) machine(now time.Time) api.Machine {
	m := api.Machine{ID: d.ID, DisplayID: d.DisplayID, Name: d.Name, Platform: d.Platform, PublicIP: d.PublicIP, Revoked: d.Revoked, CreatedAt: time.Unix(d.Created, 0).UTC(), LastSeen: time.Unix(d.Seen, 0).UTC()}
	m.Online = !m.Revoked && now.Sub(m.LastSeen) < 90*time.Second
	return m
}
func (s *Store) oneMachine(ctx context.Context, filter bson.M) (api.Machine, error) {
	var d machineDoc
	err := s.c("machines").FindOne(ctx, filter).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return api.Machine{}, ErrNotFound
	}
	if err != nil {
		return api.Machine{}, err
	}
	return d.machine(s.Now()), nil
}
func (s *Store) Machines(ctx context.Context, owner string) ([]api.Machine, error) {
	cur, err := s.c("machines").Find(ctx, bson.M{"owner_id": owner}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "display_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []machineDoc
	if err = cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := []api.Machine{}
	for _, d := range docs {
		out = append(out, d.machine(s.Now()))
	}
	return out, nil
}
func (s *Store) OwnedMachine(ctx context.Context, owner, displayID string) (api.Machine, error) {
	return s.oneMachine(ctx, bson.M{"owner_id": owner, "display_id": displayID})
}
func (s *Store) MachineAuth(ctx context.Context, credential string) (api.Machine, error) {
	m, err := s.oneMachine(ctx, bson.M{"credential_hash": auth.Hash(credential), "revoked": false})
	if errors.Is(err, ErrNotFound) {
		return m, ErrUnauthorized
	}
	return m, err
}
func (s *Store) Heartbeat(ctx context.Context, machineID, ip string) error {
	r, err := s.c("machines").UpdateOne(ctx, bson.M{"_id": machineID, "revoked": false}, bson.M{"$set": bson.M{"last_seen": s.Now().Unix(), "public_ip": ip}})
	if err != nil {
		return err
	}
	if r.MatchedCount != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RevokeMachine(ctx context.Context, owner, displayID string) error {
	r, err := s.c("machines").UpdateOne(ctx, bson.M{"owner_id": owner, "display_id": displayID}, bson.M{"$set": bson.M{"revoked": true}})
	if err != nil {
		return err
	}
	if r.MatchedCount != 1 {
		return ErrNotFound
	}
	return nil
}
