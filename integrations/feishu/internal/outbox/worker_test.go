package outbox

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

type source struct {
	sample domain.Snapshot
	err    error
	wait   bool
}

func (s *source) Fetch(ctx context.Context, _ int64) (domain.Snapshot, error) {
	if s.wait {
		<-ctx.Done()
		return domain.Snapshot{}, ctx.Err()
	}
	return s.sample, s.err
}

type sender struct {
	calls int
	err   error
	wait  bool
}

func (s *sender) AuthorizeURL(string) string { return "" }
func (s *sender) Exchange(context.Context, string) (domain.Identity, error) {
	return domain.Identity{}, nil
}
func (s *sender) Send(ctx context.Context, _ domain.Message) (string, error) {
	s.calls++
	if s.wait {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "message-1", s.err
}
func fixture(t *testing.T) (*sql.DB, *source, *sender, *Worker, *time.Time) {
	t.Helper()
	db, e := store.Open(filepath.Join(t.TempDir(), "queue.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Unix(1800000000, 0)
	_, e = db.Exec(`INSERT INTO bindings VALUES(1,'tenant','app','open','name',1,1,1);INSERT INTO preferences VALUES(1,1,5000000,1000000,1);INSERT INTO notification_outbox(event_id,user_id,binding_version,rule_version,cycle,level,balance_micros,threshold_micros,sampled_at,next_at,created_at) VALUES('e',1,1,1,0,1,4000000,5000000,?,0,0)`, now.Unix())
	if e != nil {
		t.Fatal(e)
	}
	src := &source{sample: domain.Snapshot{UserID: 1, Balance: 4000000, Active: true, Currency: "USD", At: now}}
	send := &sender{}
	w := New(db, src, send, Config{Now: func() time.Time { return now }, SendingEnabled: true, AllowedUsers: map[int64]bool{1: true}})
	return db, src, send, w, &now
}
func status(t *testing.T, db *sql.DB) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM notification_outbox WHERE event_id='e'`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestDeliveryAndDryRun(t *testing.T) {
	for _, real := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry", true: "real"}[real], func(t *testing.T) {
			db, _, send, w, _ := fixture(t)
			w.cfg.SendingEnabled = real
			for i := 0; i < 2; i++ {
				if err := w.Drain(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			want := "dry_run"
			calls := 0
			if real {
				want = "sent"
				calls = 1
			}
			if status(t, db) != want || send.calls != calls {
				t.Fatal(status(t, db), send.calls)
			}
			if real {
				var id string
				db.QueryRow(`SELECT message_id FROM notification_outbox`).Scan(&id)
				if id != "message-1" {
					t.Fatal(id)
				}
			}
		})
	}
}
func TestRevalidation(t *testing.T) {
	for _, kind := range []string{"binding", "rule", "disabled", "inactive_binding", "inactive_user", "stale", "wrong_user", "recovered", "allowlist", "fetch"} {
		t.Run(kind, func(t *testing.T) {
			db, src, send, w, now := fixture(t)
			var sql string
			switch kind {
			case "binding":
				sql = `UPDATE bindings SET version=2`
			case "rule":
				sql = `UPDATE preferences SET version=2`
			case "disabled":
				sql = `UPDATE preferences SET enabled=0`
			case "inactive_binding":
				sql = `UPDATE bindings SET active=0`
			case "inactive_user":
				src.sample.Active = false
			case "stale":
				src.sample.At = now.Add(-6 * time.Minute)
			case "wrong_user":
				src.sample.UserID = 2
			case "recovered":
				src.sample.Balance = 6000000
			case "allowlist":
				w.cfg.AllowedUsers = nil
			case "fetch":
				src.err = errors.New("failed")
			}
			if sql != "" {
				if _, err := db.Exec(sql); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			if send.calls != 0 {
				t.Fatal("sent invalid event")
			}
			want := "cancelled"
			if kind == "stale" || kind == "wrong_user" || kind == "fetch" {
				want = "pending"
			}
			if s := status(t, db); s != want {
				t.Fatal(s)
			}
		})
	}
}
func TestExpiredLeaseIsUncertain(t *testing.T) {
	db, _, send, w, now := fixture(t)
	if _, err := db.Exec(`UPDATE notification_outbox SET status='leased',lease_until=?`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status(t, db) != "uncertain" || send.calls != 0 {
		t.Fatal("lease replay")
	}
}
func TestErrorClassification(t *testing.T) {
	for _, tc := range []struct{ kind, status string }{{"rate_limit", "pending"}, {"transient", "pending"}, {"uncertain", "uncertain"}, {"permission", "failed"}, {"recipient", "failed"}, {"token", "pending"}, {"protocol", "failed"}, {"network", "uncertain"}} {
		t.Run(tc.kind, func(t *testing.T) {
			db, _, send, w, now := fixture(t)
			send.err = &domain.SendError{Kind: tc.kind, RetryAfter: 30 * time.Second}
			if err := w.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			if status(t, db) != tc.status {
				t.Fatal(status(t, db))
			}
			if tc.status == "pending" {
				var next int64
				db.QueryRow(`SELECT next_at FROM notification_outbox`).Scan(&next)
				if next < now.Add(30*time.Second).Unix() {
					t.Fatal("retry too early")
				}
			}
		})
	}
}
func TestRetryExhausted(t *testing.T) {
	db, _, send, w, _ := fixture(t)
	send.err = &domain.SendError{Kind: "rate_limit"}
	if _, err := db.Exec(`UPDATE notification_outbox SET attempts=4`); err != nil {
		t.Fatal(err)
	}
	if err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status(t, db) != "failed" || send.calls != 1 {
		t.Fatal("unbounded retry")
	}
}

func TestTimeoutPersistsUncertain(t *testing.T) {
	db, _, send, w, _ := fixture(t)
	send.wait = true
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := w.Drain(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if status(t, db) != "uncertain" {
		t.Fatal(status(t, db))
	}
	w.lastSend = time.Time{}
	if err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if send.calls != 1 {
		t.Fatal("timeout replay")
	}
}

func TestBalanceOutageThenRecoveryDeliversSameEvent(t *testing.T) {
	db, src, send, w, now := fixture(t)
	src.err = errors.New("temporary outage")
	if err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status(t, db) != "pending" || send.calls != 0 {
		t.Fatal("outage lost event or sent")
	}
	var next int64
	if err := db.QueryRow(`SELECT next_at FROM notification_outbox WHERE event_id='e'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next <= now.Unix() {
		t.Fatal("missing backoff")
	}
	*now = time.Unix(next, 0)
	src.err = nil
	src.sample.At = *now
	if err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status(t, db) != "sent" || send.calls != 1 {
		t.Fatal("recovery did not send")
	}
	var attempts int
	db.QueryRow(`SELECT attempts FROM notification_outbox WHERE event_id='e'`).Scan(&attempts)
	if attempts != 2 {
		t.Fatal(attempts)
	}
}
func TestBalanceValidationRetryBounded(t *testing.T) {
	for _, kind := range []string{"fetch", "stale"} {
		t.Run(kind, func(t *testing.T) {
			db, src, send, w, now := fixture(t)
			if kind == "fetch" {
				src.err = errors.New("offline")
			} else {
				src.sample.At = now.Add(-time.Hour)
			}
			for i := 1; i <= 5; i++ {
				if err := w.Drain(context.Background()); err != nil {
					t.Fatal(err)
				}
				want := "pending"
				if i == 5 {
					want = "failed"
				}
				if status(t, db) != want {
					t.Fatalf("attempt %d: %s", i, status(t, db))
				}
				*now = now.Add(time.Minute)
			}
			if send.calls != 0 {
				t.Fatal("invalid balance sent")
			}
		})
	}
}
func TestTimeoutBeforeSendIsRetryable(t *testing.T) {
	db, src, send, w, _ := fixture(t)
	src.wait = true
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := w.Drain(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if status(t, db) != "pending" || send.calls != 0 {
		t.Fatal("pre-send timeout marked uncertain or sent")
	}
}
