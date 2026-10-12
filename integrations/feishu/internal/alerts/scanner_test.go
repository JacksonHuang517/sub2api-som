package alerts

import (
	"context"
	"database/sql"
	"errors"
	"som/feishu/internal/domain"
	"som/feishu/internal/store"
	"testing"
	"time"
)

type source struct {
	sample domain.Snapshot
	err    error
}

func (s *source) Fetch(context.Context, int64) (domain.Snapshot, error) { return s.sample, s.err }
func fixture(t *testing.T) (*sql.DB, *source, *Scanner, *time.Time) {
	t.Helper()
	db, e := store.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	_, e = db.Exec(`INSERT INTO bindings VALUES(1,'tenant','app','open','name',1,1,1);INSERT INTO preferences VALUES(1,1,5000000,1000000,1)`)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Unix(1800000000, 0)
	src := &source{sample: domain.Snapshot{UserID: 1, Currency: "USD", Active: true, At: now, Balance: 5000001}}
	s := New(db, src, Config{Now: func() time.Time { return now }})
	return db, src, s, &now
}
func count(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func scan(t *testing.T, s *Scanner, src *source, now *time.Time, balance int64) {
	t.Helper()
	*now = now.Add(time.Second)
	src.sample.At = *now
	src.sample.Balance = balance
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestBoundariesAndRestart(t *testing.T) {
	db, src, s, now := fixture(t)
	for _, tc := range []struct {
		v int64
		n int
	}{{5000001, 0}, {5000000, 1}, {1000001, 1}, {1000000, 2}, {1, 2}, {0, 3}, {-1, 3}} {
		scan(t, s, src, now, tc.v)
		if n := count(t, db); n != tc.n {
			t.Fatalf("balance %d count %d", tc.v, n)
		}
	}
	s = New(db, src, Config{Now: func() time.Time { return *now }})
	scan(t, s, src, now, 0)
	if count(t, db) != 3 {
		t.Fatal("restart replay")
	}
}
func TestRecoveryRequiresTwoStrictSamples(t *testing.T) {
	db, src, s, now := fixture(t)
	scan(t, s, src, now, 5000000)
	scan(t, s, src, now, 5100001)
	scan(t, s, src, now, 5100000)
	scan(t, s, src, now, 5000000)
	if count(t, db) != 1 {
		t.Fatal("early recovery")
	}
	scan(t, s, src, now, 5100001)
	scan(t, s, src, now, 5100001)
	scan(t, s, src, now, 5000000)
	if count(t, db) != 2 {
		t.Fatal("new cycle missing")
	}
}
func TestRuleAndBindingPreserveCycle(t *testing.T) {
	db, src, s, now := fixture(t)
	scan(t, s, src, now, 5000000)
	if _, err := db.Exec(`UPDATE preferences SET version=2;UPDATE bindings SET version=2`); err != nil {
		t.Fatal(err)
	}
	scan(t, s, src, now, 5000000)
	if count(t, db) != 1 {
		t.Fatal("rule replay")
	}
	var status string
	db.QueryRow(`SELECT status FROM notification_outbox`).Scan(&status)
	if status != "cancelled" {
		t.Fatal(status)
	}
}
func TestInvalidSamplesNeverCreateAlert(t *testing.T) {
	for _, kind := range []string{"stale", "future", "wrong_user", "currency", "fetch", "inactive"} {
		t.Run(kind, func(t *testing.T) {
			db, src, s, now := fixture(t)
			src.sample.Balance = 0
			switch kind {
			case "stale":
				src.sample.At = now.Add(-6 * time.Minute)
			case "future":
				src.sample.At = now.Add(time.Minute)
			case "wrong_user":
				src.sample.UserID = 2
			case "currency":
				src.sample.Currency = "CNY"
			case "fetch":
				src.err = errors.New("upstream")
			case "inactive":
				src.sample.Active = false
			}
			err := s.Scan(context.Background())
			if kind != "inactive" && err == nil {
				t.Fatal("accepted invalid sample")
			}
			if count(t, db) != 0 {
				t.Fatal("false zero alert")
			}
		})
	}
}
func TestOutOfOrderAndDisabled(t *testing.T) {
	db, src, s, now := fixture(t)
	scan(t, s, src, now, 6000000)
	src.sample.Balance = 0
	if s.Scan(context.Background()) == nil {
		t.Fatal("accepted duplicate")
	}
	if _, err := db.Exec(`UPDATE preferences SET enabled=0`); err != nil {
		t.Fatal(err)
	}
	scan(t, s, src, now, 0)
	if count(t, db) != 0 {
		t.Fatal("disabled alert")
	}
}

func TestEscalationSupersedesPendingLowerLevels(t *testing.T) {
	db, src, s, now := fixture(t)
	for _, tc := range []struct {
		balance int64
		level   int
	}{{4000000, 1}, {500000, 2}, {0, 3}} {
		scan(t, s, src, now, tc.balance)
		var pending, level int
		if err := db.QueryRow(`SELECT COUNT(*),MAX(level) FROM notification_outbox WHERE status='pending'`).Scan(&pending, &level); err != nil {
			t.Fatal(err)
		}
		if pending != 1 || level != tc.level {
			t.Fatalf("pending=%d level=%d", pending, level)
		}
	}
	var superseded int
	db.QueryRow(`SELECT COUNT(*) FROM notification_outbox WHERE status='cancelled' AND last_error='LEVEL_SUPERSEDED'`).Scan(&superseded)
	if superseded != 2 {
		t.Fatal(superseded)
	}
}
func TestEscalationPreservesDeliveredLedger(t *testing.T) {
	db, src, s, now := fixture(t)
	scan(t, s, src, now, 4000000)
	if _, err := db.Exec(`UPDATE notification_outbox SET status='sent',message_id='already-sent'`); err != nil {
		t.Fatal(err)
	}
	scan(t, s, src, now, 0)
	var sent int
	db.QueryRow(`SELECT COUNT(*) FROM notification_outbox WHERE status='sent' AND message_id='already-sent'`).Scan(&sent)
	if sent != 1 {
		t.Fatal("escalation altered sent ledger")
	}
}
