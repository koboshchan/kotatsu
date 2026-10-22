package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/koboshchan/kotatsu/internal/api"
	"github.com/koboshchan/kotatsu/internal/auth"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"math/big"
	"time"
)

var ErrUnauthorized = errors.New("invalid or revoked credential")
var ErrConflict = errors.New("name already registered")
var ErrNotFound = errors.New("not found")

type Identity struct {
	Account api.Account
	TokenID string
}

// Store keeps all state in MongoDB. The documented deployment is a private
// Docker network MongoDB without authentication and without published ports.
type Store struct {
	client       *mongo.Client
	db           *mongo.Database
	Now          func() time.Time
	NewDisplayID func() (string, error)
}

const dbTimeout = 10 * time.Second

func (s *Store) c(name string) *mongo.Collection { return s.db.Collection(name) }

func Open(uri, database string) (*Store, error) {
	if uri == "" || database == "" {
		return nil, errors.New("MongoDB URI and database name are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(20 * time.Second))
	if err != nil {
		return nil, err
	}
	s := &Store{client: client, db: client.Database(database), Now: time.Now, NewDisplayID: randomDisplayID}
	if err = client.Ping(ctx, nil); err != nil {
		client.Disconnect(context.Background())
		return nil, err
	}
	unique := func(coll string, keys bson.D) mongo.IndexModel {
		return mongo.IndexModel{Keys: keys, Options: options.Index().SetUnique(true)}
	}
	plain := func(keys bson.D) mongo.IndexModel { return mongo.IndexModel{Keys: keys} }
	indexes := map[string][]mongo.IndexModel{
		"accounts":          {unique("accounts", bson.D{{Key: "name", Value: 1}})},
		"account_tokens":    {unique("", bson.D{{Key: "hash", Value: 1}}), plain(bson.D{{Key: "owner_id", Value: 1}})},
		"enrollment_tokens": {unique("", bson.D{{Key: "hash", Value: 1}}), plain(bson.D{{Key: "owner_id", Value: 1}})},
		"machines":          {unique("", bson.D{{Key: "display_id", Value: 1}}), unique("", bson.D{{Key: "credential_hash", Value: 1}}), plain(bson.D{{Key: "owner_id", Value: 1}})},
		"sessions":          {plain(bson.D{{Key: "ended_at", Value: 1}})},
	}
	for name, models := range indexes {
		if _, err = s.c(name).Indexes().CreateMany(ctx, models); err != nil {
			client.Disconnect(context.Background())
			return nil, err
		}
	}
	// Relays are deliberately transient; after a restart no historical session
	// remains marked active.
	_, err = s.c("sessions").UpdateMany(ctx, bson.M{"ended_at": nil}, bson.M{"$set": bson.M{"ended_at": s.Now().Unix(), "reason": "server_restart"}})
	if err != nil {
		client.Disconnect(context.Background())
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	return s.client.Disconnect(ctx)
}

// DropDatabase is only for tests that own a unique database.
func (s *Store) DropDatabase(ctx context.Context) error { return s.db.Drop(ctx) }

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
	if _, err = s.c("accounts").InsertOne(ctx, bson.M{"_id": out.Account.ID, "name": name, "created_at": s.Now().Unix()}); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return api.Registration{}, ErrConflict
		}
		return api.Registration{}, err
	}
	_, err = s.c("account_tokens").InsertOne(ctx, bson.M{"_id": out.Token.ID, "owner_id": out.Account.ID, "hash": auth.Hash(raw), "created_at": out.Token.CreatedAt.Unix(), "revoked": false})
	if err != nil {
		// No transactions on a standalone MongoDB: undo the account so the name is not lost.
		s.c("accounts").DeleteOne(context.WithoutCancel(ctx), bson.M{"_id": out.Account.ID})
		return api.Registration{}, err
	}
	return out, nil
}
func (s *Store) Authenticate(ctx context.Context, token string) (Identity, error) {
	var i Identity
	var t struct {
		ID    string `bson:"_id"`
		Owner string `bson:"owner_id"`
	}
	err := s.c("account_tokens").FindOne(ctx, bson.M{"hash": auth.Hash(token), "revoked": false}).Decode(&t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return i, ErrUnauthorized
	}
	if err != nil {
		return i, err
	}
	var a struct {
		Name string `bson:"name"`
	}
	err = s.c("accounts").FindOne(ctx, bson.M{"_id": t.Owner}).Decode(&a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return i, ErrUnauthorized
	}
	if err != nil {
		return i, err
	}
	i.Account = api.Account{ID: t.Owner, Name: a.Name}
	i.TokenID = t.ID
	return i, nil
}
func (s *Store) Tokens(ctx context.Context, owner string) ([]api.Token, error) {
	cur, err := s.c("account_tokens").Find(ctx, bson.M{"owner_id": owner, "revoked": false}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID      string `bson:"_id"`
		Created int64  `bson:"created_at"`
	}
	if err = cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := []api.Token{}
	for _, d := range docs {
		out = append(out, api.Token{ID: d.ID, CreatedAt: time.Unix(d.Created, 0).UTC()})
	}
	return out, nil
}
func (s *Store) IssueToken(ctx context.Context, owner, rotateID string) (api.Token, error) {
	raw, err := auth.New("as")
	if err != nil {
		return api.Token{}, err
	}
	out := api.Token{ID: uuid.NewString(), Token: raw, CreatedAt: s.Now().UTC()}
	if rotateID != "" {
		// Revoke atomically first so only one concurrent rotation can win.
		if err = s.RevokeToken(ctx, owner, rotateID); err != nil {
			return api.Token{}, err
		}
	}
	_, err = s.c("account_tokens").InsertOne(ctx, bson.M{"_id": out.ID, "owner_id": owner, "hash": auth.Hash(raw), "created_at": out.CreatedAt.Unix(), "revoked": false})
	if err != nil {
		if rotateID != "" {
			s.c("account_tokens").UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": rotateID, "owner_id": owner}, bson.M{"$set": bson.M{"revoked": false}})
		}
		return api.Token{}, err
	}
	return out, nil
}
func (s *Store) RevokeToken(ctx context.Context, owner, id string) error {
	r, err := s.c("account_tokens").UpdateOne(ctx, bson.M{"owner_id": owner, "_id": id, "revoked": false}, bson.M{"$set": bson.M{"revoked": true}})
	if err != nil {
		return err
	}
	if r.ModifiedCount != 1 {
		return ErrNotFound
	}
	return nil
}
