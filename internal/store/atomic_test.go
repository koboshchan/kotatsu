package store

import (
	"context"
	"github.com/koboshchan/kotatsu/internal/api"
	"go.mongodb.org/mongo-driver/v2/bson"
	"sync"
	"testing"
	"time"
)

func rejectInserts(t *testing.T, s *Store, name string) {
	t.Helper()
	err := s.db.RunCommand(context.Background(), bson.D{{Key: "collMod", Value: name}, {Key: "validator", Value: bson.M{"reject_test": bson.M{"$exists": true}}}, {Key: "validationLevel", Value: "strict"}}).Err()
	if err != nil {
		t.Fatal(err)
	}
}
func TestRegistrationRollback(t *testing.T) {
	s := setup(t)
	rejectInserts(t, s, "account_tokens")
	if _, err := s.Register(context.Background(), "atomic"); err == nil {
		t.Fatal("expected rejected token")
	}
	n, err := s.c("accounts").CountDocuments(context.Background(), bson.M{"name": "atomic"})
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
func TestRotationRollback(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a, err := s.Register(ctx, "rotate")
	if err != nil {
		t.Fatal(err)
	}
	rejectInserts(t, s, "account_tokens")
	if _, err = s.IssueToken(ctx, a.Account.ID, a.Token.ID); err == nil {
		t.Fatal("expected rejected token")
	}
	if _, err = s.Authenticate(ctx, a.Token.Token); err != nil {
		t.Fatal("rotation lost original credential", err)
	}
}
func TestConcurrentSingleUseAndCollision(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a, err := s.Register(ctx, "race")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateEnrollment(ctx, a.Account.ID, false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ch := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Enroll(ctx, api.EnrollRequest{Token: token.Token, Name: "race", Platform: "linux"}, "")
			ch <- err
		}()
	}
	wg.Wait()
	close(ch)
	wins := 0
	for err := range ch {
		if err == nil {
			wins++
		} else if err != ErrUnauthorized {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("single-use winners", wins)
	}
	ms, err := s.Machines(ctx, a.Account.ID)
	if err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	token, err = s.CreateEnrollment(ctx, a.Account.ID, false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.NewDisplayID = func() (string, error) {
		calls++
		if calls == 1 {
			return ms[0].DisplayID, nil
		}
		return "7654321", nil
	}
	if _, err = s.Enroll(ctx, api.EnrollRequest{Token: token.Token, Name: "collision", Platform: "linux"}, ""); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("did not retry collision", calls)
	}
	es, err := s.Enrollments(ctx, a.Account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.ID == token.ID && e.Used != 1 {
			t.Fatal("collision consumed token twice", e.Used)
		}
	}
}
