package subscriptions

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"som/feishu/internal/domain"
	"som/feishu/internal/store"
	"testing"
	"time"
)

type fixture struct {
	sample  domain.SubscriptionSnapshot
	err     error
	sent    []domain.Message
	sendErr error
}

func (f *fixture) FetchSubscriptions(_ context.Context, id int64) (domain.SubscriptionSnapshot, error) {
	s := f.sample
	s.UserID = id
	return s, f.err
}
func (f *fixture) AuthorizeURL(string) string { return "" }
func (f *fixture) Exchange(context.Context, string) (domain.Identity, error) {
	return domain.Identity{}, nil
}
func (f *fixture) Send(_ context.Context, m domain.Message) (string, error) {
	f.sent = append(f.sent, m)
	return "message", f.sendErr
}
func snapshot(now time.Time, used int64) domain.SubscriptionSnapshot {
	limit := int64(100000000)
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	start := now.Add(-time.Hour)
	reset := now.Add(24 * time.Hour)
	return domain.SubscriptionSnapshot{UserID: 1, Active: true, Currency: "USD", At: now, Subscriptions: []domain.SubscriptionQuota{{SubscriptionID: 10, GroupID: 2, GroupName: "quota", Status: "active", ExpiresAt: now.Add(30 * 24 * time.Hour), Windows: []domain.QuotaWindow{{Kind: "daily", Limited: true, LimitMicros: &limit, UsedMicros: used, RemainingMicros: &remaining, WindowStart: &start, ResetAt: &reset}, {Kind: "weekly"}, {Kind: "monthly"}}}}}
}
func setup(t *testing.T) (*sql.DB, *fixture, *Scanner, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	db, e := store.Open(filepath.Join(t.TempDir(), "subscription.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	seed(t, db, 1)
	f := &fixture{sample: snapshot(now, 81000000)}
	return db, f, NewScanner(db, f, func() time.Time { return now }), now
}
func seed(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	if _, e := db.Exec(`INSERT INTO bindings VALUES(?,'tenant','app',?,'name',1,1,1)`, id, "open-"+string(rune(id))); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO subscription_preferences(user_id,enabled) VALUES(?,1)`, id); e != nil {
		t.Fatal(e)
	}
}
func count(t *testing.T, db *sql.DB, status string) int {
	t.Helper()
	var n int
	if e := db.QueryRow(`SELECT COUNT(*) FROM subscription_outbox WHERE status=?`, status).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func scan(t *testing.T, s *Scanner) {
	t.Helper()
	if e := s.Scan(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestDurableDedupAndUserSubscriptionIsolation(t *testing.T) {
	db, f, s, now := setup(t)
	seed(t, db, 2)
	second := f.sample.Subscriptions[0]
	second.SubscriptionID = 11
	f.sample.Subscriptions = append(f.sample.Subscriptions, second)
	scan(t, s)
	if count(t, db, "pending") != 4 {
		t.Fatal("users/subscriptions collided")
	}
	// Reopen the actual database, retaining binding and dedup state.
	var path string
	var seq int
	var name string
	if e := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); e != nil {
		t.Fatal(e)
	}
	db.Close()
	again, e := store.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer again.Close()
	f.sample.At = now.Add(time.Second)
	scan(t, NewScanner(again, f, func() time.Time { return now.Add(time.Second) }))
	if count(t, again, "pending") != 4 {
		t.Fatal("restart duplicated")
	}
}
func TestUpgradeWeeklyResetAndRuleChange(t *testing.T) {
	db, f, s, now := setup(t)
	weekly := f.sample.Subscriptions[0].Windows[0]
	weekly.Kind = "weekly"
	f.sample.Subscriptions[0].Windows[1] = weekly
	scan(t, s)
	f.sample.At = now.Add(time.Second)
	for i := 0; i < 2; i++ {
		f.sample.Subscriptions[0].Windows[i].UsedMicros = 96000000
		remaining := int64(4000000)
		f.sample.Subscriptions[0].Windows[i].RemainingMicros = &remaining
	}
	scan(t, s)
	if count(t, db, "cancelled") != 2 || count(t, db, "pending") != 2 {
		t.Fatal("pending lower levels not superseded")
	}
	newStart := now
	f.sample.At = now.Add(2 * time.Second)
	q := &f.sample.Subscriptions[0].Windows[1]
	q.WindowStart = &newStart
	q.UsedMicros = 0
	rem := *q.LimitMicros
	q.RemainingMicros = &rem
	scan(t, s)
	if count(t, db, "pending") != 1 {
		t.Fatal("weekly reset did not cancel")
	}
	if _, e := db.Exec(`UPDATE subscription_preferences SET version=2,warning_percent=75 WHERE user_id=1`); e != nil {
		t.Fatal(e)
	}
	f.sample.At = now.Add(3 * time.Second)
	scan(t, s)
	if count(t, db, "pending") != 0 {
		t.Fatal("rule edit resent observed level")
	}
	q.UsedMicros = 81000000
	rem = 19000000
	f.sample.At = now.Add(4 * time.Second)
	scan(t, s)
	if count(t, db, "pending") != 1 {
		t.Fatal("new weekly window failed to alert")
	}
}
func TestUnavailableStaleAndOutOfOrderNeverBecomeZero(t *testing.T) {
	db, f, s, now := setup(t)
	scan(t, s)
	f.err = errors.New("fetch unavailable")
	if s.Scan(context.Background()) == nil {
		t.Fatal("fetch failure accepted")
	}
	if count(t, db, "pending") != 1 {
		t.Fatal("failure cancelled event")
	}
	f.err = nil
	if s.Scan(context.Background()) == nil {
		t.Fatal("equal sample accepted")
	}
	f.sample.At = now.Add(-6 * time.Minute)
	if s.Scan(context.Background()) == nil {
		t.Fatal("stale accepted")
	}
	f.sample.At = now.Add(time.Second)
	f.sample.Subscriptions[0].Windows[0].LimitMicros = nil
	if s.Scan(context.Background()) == nil {
		t.Fatal("missing quota accepted")
	}
	if count(t, db, "pending") != 1 {
		t.Fatal("invalid quota cancelled event")
	}
}
func TestIneligibleFullSnapshotCancelsPending(t *testing.T) {
	for _, reason := range []string{"paused", "expired", "unlimited", "removed", "user inactive"} {
		t.Run(reason, func(t *testing.T) {
			db, f, s, now := setup(t)
			scan(t, s)
			f.sample.At = now.Add(time.Second)
			switch reason {
			case "paused":
				f.sample.Subscriptions[0].Status = "suspended"
			case "expired":
				f.sample.Subscriptions[0].ExpiresAt = now
				f.sample.Subscriptions[0].Windows[0].ResetAt = nil
			case "unlimited":
				f.sample.Subscriptions[0].Windows[0] = domain.QuotaWindow{Kind: "daily"}
			case "removed":
				f.sample.Subscriptions = []domain.SubscriptionQuota{}
			case "user inactive":
				f.sample.Active = false
			}
			scan(t, s)
			if count(t, db, "pending") != 0 || count(t, db, "cancelled") != 1 {
				t.Fatal(reason)
			}
		})
	}
}
func TestSendRevalidatesCompleteSnapshotAndVersions(t *testing.T) {
	for _, reason := range []string{"recover", "upgrade", "window", "limit", "expiry", "unbound", "rule", "other user"} {
		t.Run(reason, func(t *testing.T) {
			db, f, s, now := setup(t)
			scan(t, s)
			f.sample.At = now.Add(time.Second)
			q := &f.sample.Subscriptions[0].Windows[0]
			switch reason {
			case "recover":
				q.UsedMicros = 0
				rem := *q.LimitMicros
				q.RemainingMicros = &rem
			case "upgrade":
				q.UsedMicros = 96000000
				rem := int64(4000000)
				q.RemainingMicros = &rem
			case "window":
				newStart := now
				q.WindowStart = &newStart
			case "limit":
				limit := int64(200000000)
				q.LimitMicros = &limit
				rem := limit - q.UsedMicros
				q.RemainingMicros = &rem
			case "expiry":
				f.sample.Subscriptions[0].ExpiresAt = now
				q.ResetAt = nil
			case "unbound":
				db.Exec(`UPDATE bindings SET active=0,version=2 WHERE user_id=1`)
			case "rule":
				db.Exec(`UPDATE subscription_preferences SET version=2 WHERE user_id=1`)
			case "other user":
				f.sample.Subscriptions[0].SubscriptionID = 99
			}
			w := NewWorker(db, f, f, Config{Now: func() time.Time { return now }, SendingEnabled: true, AllowedUsers: map[int64]bool{1: true}})
			if e := w.Drain(context.Background()); e != nil {
				t.Fatal(e)
			}
			if len(f.sent) != 0 || count(t, db, "cancelled") != 1 {
				t.Fatal("obsolete event delivered")
			}
		})
	}
}
func TestBoundedRetryAndUncertainAreNotDuplicated(t *testing.T) {
	db, f, s, now := setup(t)
	scan(t, s)
	f.err = errors.New("unavailable")
	w := NewWorker(db, f, f, Config{Now: func() time.Time { return now }, SendingEnabled: true, AllowedUsers: map[int64]bool{1: true}})
	for i := 0; i < 5; i++ {
		db.Exec(`UPDATE subscription_outbox SET next_at=0 WHERE status='pending'`)
		if e := w.Drain(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if count(t, db, "failed") != 1 || len(f.sent) != 0 {
		t.Fatal("unbounded validation retry")
	}
	db.Exec(`UPDATE subscription_outbox SET status='leased',lease_until=0`)
	f.err = nil
	if e := w.Drain(context.Background()); e != nil {
		t.Fatal(e)
	}
	if count(t, db, "uncertain") != 1 || len(f.sent) != 0 {
		t.Fatal("expired lease duplicated")
	}
}
func TestSuccessfulSendIncludesCurrentQuota(t *testing.T) {
	db, f, s, now := setup(t)
	scan(t, s)
	f.sample.At = now.Add(time.Second)
	q := &f.sample.Subscriptions[0].Windows[0]
	q.UsedMicros = 82000000
	rem := int64(18000000)
	q.RemainingMicros = &rem
	if e := NewWorker(db, f, f, Config{Now: func() time.Time { return now }, SendingEnabled: true, AllowedUsers: map[int64]bool{1: true}}).Drain(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(f.sent) != 1 || count(t, db, "sent") != 1 || f.sent[0].Subscription.Window.UsedMicros != 82000000 {
		t.Fatal("send did not use live data")
	}
}

func TestExhaustionAcrossAllWindows(t *testing.T) {
	db, f, s, _ := setup(t)
	for i, kind := range []string{"daily", "weekly", "monthly"} {
		q := f.sample.Subscriptions[0].Windows[0]
		q.Kind = kind
		q.UsedMicros = 100000001
		remaining := int64(0)
		q.RemainingMicros = &remaining
		f.sample.Subscriptions[0].Windows[i] = q
	}
	scan(t, s)
	var n int
	if e := db.QueryRow(`SELECT COUNT(*) FROM subscription_outbox WHERE level=3 AND status='pending'`).Scan(&n); e != nil || n != 3 {
		t.Fatal("exhaustion/window isolation", n, e)
	}
}
func TestCurrentClockExpiryAndUncertainSend(t *testing.T) {
	t.Run("current expiry", func(t *testing.T) {
		db, f, s, now := setup(t)
		scan(t, s)
		f.sample.At = now.Add(time.Second)
		f.sample.Subscriptions[0].ExpiresAt = now.Add(2 * time.Second)
		f.sample.Subscriptions[0].Windows[0].ResetAt = nil
		w := NewWorker(db, f, f, Config{Now: func() time.Time { return now.Add(3 * time.Second) }, SendingEnabled: true, AllowedUsers: map[int64]bool{1: true}})
		if e := w.Drain(context.Background()); e != nil {
			t.Fatal(e)
		}
		if len(f.sent) != 0 || count(t, db, "cancelled") != 1 {
			t.Fatal("sent after current expiry")
		}
	})
	t.Run("uncertain send", func(t *testing.T) {
		db, f, s, now := setup(t)
		scan(t, s)
		f.sendErr = errors.New("unknown send outcome")
		w := NewWorker(db, f, f, Config{Now: func() time.Time { return now }, SendingEnabled: true, AllowedUsers: map[int64]bool{1: true}})
		for i := 0; i < 2; i++ {
			if e := w.Drain(context.Background()); e != nil {
				t.Fatal(e)
			}
		}
		if len(f.sent) != 1 || count(t, db, "uncertain") != 1 {
			t.Fatal("uncertain send retried")
		}
	})
}

type blockingSender struct{ entered, release chan struct{} }

func (b *blockingSender) AuthorizeURL(string) string { return "" }
func (b *blockingSender) Exchange(context.Context, string) (domain.Identity, error) {
	return domain.Identity{}, nil
}
func (b *blockingSender) Send(ctx context.Context, _ domain.Message) (string, error) {
	close(b.entered)
	select {
	case <-b.release:
		return "message", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func TestDeliverySerializesDisable(t *testing.T) {
	db, f, s, now := setup(t)
	scan(t, s)
	sender := &blockingSender{make(chan struct{}), make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- NewWorker(db, f, sender, Config{Now: func() time.Time { return now }, SendingEnabled: true, AllowedUsers: map[int64]bool{1: true}}).Drain(context.Background())
	}()
	<-sender.entered
	disabled := make(chan error, 1)
	go func() {
		_, e := db.Exec(`UPDATE subscription_preferences SET enabled=0,version=version+1 WHERE user_id=1`)
		disabled <- e
	}()
	select {
	case <-disabled:
		t.Fatal("disable passed in-flight transaction")
	case <-time.After(20 * time.Millisecond):
	}
	close(sender.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := <-disabled; e != nil {
		t.Fatal(e)
	}
	if count(t, db, "sent") != 1 {
		t.Fatal("send not finalized before disable")
	}
}
