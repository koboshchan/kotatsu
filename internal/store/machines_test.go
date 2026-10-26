package store

import (
	"context"
	"fmt"
	"github.com/koboshchan/kotatsu/internal/api"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnrollmentInventoryHeartbeat(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	s.Now = func() time.Time { return now }
	a, _ := s.Register(ctx, "alice")
	b, _ := s.Register(ctx, "bob")
	token, e := s.CreateEnrollment(ctx, a.Account.ID, false, 15*time.Minute, false)
	if e != nil {
		t.Fatal(e)
	}
	m, e := s.Enroll(ctx, api.EnrollRequest{Token: token.Token, Name: "one", Platform: "linux/amd64"}, "192.0.2.1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Enroll(ctx, api.EnrollRequest{Token: token.Token, Name: "two"}, "1"); e != ErrUnauthorized {
		t.Fatal("reuse", e)
	}
	if _, e = s.MachineAuth(ctx, a.Token.Token); e != ErrUnauthorized {
		t.Fatal("kind", e)
	}
	if _, e = s.OwnedMachine(ctx, b.Account.ID, m.Machine.DisplayID); e != ErrNotFound {
		t.Fatal("IDOR", e)
	}
	list, _ := s.Machines(ctx, b.Account.ID)
	if len(list) != 0 {
		t.Fatal("leak")
	}
	list, _ = s.Machines(ctx, a.Account.ID)
	if len(list) != 1 || !list[0].Online || list[0].PublicIP != "192.0.2.1" {
		t.Fatal(list)
	}
	now = now.Add(90 * time.Second)
	list, _ = s.Machines(ctx, a.Account.ID)
	if list[0].Online {
		t.Fatal("expiry")
	}
	if e = s.Heartbeat(ctx, m.Machine.ID, "192.0.2.2"); e != nil {
		t.Fatal(e)
	}
	list, _ = s.Machines(ctx, a.Account.ID)
	if !list[0].Online || list[0].PublicIP != "192.0.2.2" {
		t.Fatal(list)
	}
	expired, _ := s.CreateEnrollment(ctx, a.Account.ID, false, time.Second, false)
	now = now.Add(time.Second)
	if e = s.EnrollmentValid(ctx, expired.Token); e != ErrUnauthorized {
		t.Fatal("expiration", e)
	}
	revoked, _ := s.CreateEnrollment(ctx, a.Account.ID, false, time.Minute, false)
	if e = s.RevokeEnrollment(ctx, b.Account.ID, revoked.ID); e != ErrNotFound {
		t.Fatal("IDOR")
	}
	_ = s.RevokeEnrollment(ctx, a.Account.ID, revoked.ID)
	if e = s.EnrollmentValid(ctx, revoked.Token); e != ErrUnauthorized {
		t.Fatal("revoked")
	}
	reusable, _ := s.CreateEnrollment(ctx, a.Account.ID, true, time.Minute, false)
	for n := 0; n < 2; n++ {
		if _, e = s.Enroll(ctx, api.EnrollRequest{Token: reusable.Token, Name: "reuse"}, "1"); e != nil {
			t.Fatal(e)
		}
	}
}
func TestCollisionAndSingleRedemption(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a, _ := s.Register(ctx, "owner")
	s.NewDisplayID = func() (string, error) { return "1234567", nil }
	one, _ := s.CreateEnrollment(ctx, a.Account.ID, false, time.Minute, false)
	_, e := s.Enroll(ctx, api.EnrollRequest{Token: one.Token, Name: "one"}, "1")
	if e != nil {
		t.Fatal(e)
	}
	var counter atomic.Int32
	s.NewDisplayID = func() (string, error) {
		n := counter.Add(1)
		if n == 1 {
			return "1234567", nil
		}
		return fmt.Sprintf("%07d", 1234567+n), nil
	}
	two, _ := s.CreateEnrollment(ctx, a.Account.ID, false, time.Minute, false)
	var wg sync.WaitGroup
	success := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Enroll(ctx, api.EnrollRequest{Token: two.Token, Name: "two"}, "1")
			success <- e == nil
		}()
	}
	wg.Wait()
	close(success)
	count := 0
	for ok := range success {
		if ok {
			count++
		}
	}
	if count != 1 || counter.Load() < 2 {
		t.Fatal(count, counter.Load())
	}
}
