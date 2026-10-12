package subscriptions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"som/feishu/internal/domain"
	"sync"
	"time"
)

type Scanner struct {
	db     *sql.DB
	source domain.Subscriptions
	now    func() time.Time
	mu     sync.Mutex
	cursor int64
}

func NewScanner(db *sql.DB, source domain.Subscriptions, now func() time.Time) *Scanner {
	if now == nil {
		now = time.Now
	}
	return &Scanner{db: db, source: source, now: now}
}

type candidate struct{ uid, binding, rule, warning, critical int64 }

func (s *Scanner) Scan(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, e := s.db.QueryContext(ctx, `SELECT b.user_id,b.version,p.version,p.warning_percent,p.critical_percent FROM bindings b JOIN subscription_preferences p ON p.user_id=b.user_id WHERE b.active=1 AND p.enabled=1 AND b.user_id>? ORDER BY b.user_id LIMIT 100`, s.cursor)
	if e != nil {
		return e
	}
	var users []candidate
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.uid, &c.binding, &c.rule, &c.warning, &c.critical); e != nil {
			rows.Close()
			return e
		}
		users = append(users, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	s.cursor = 0
	if len(users) == 100 {
		s.cursor = users[99].uid
	}
	var failures []error
	for _, c := range users {
		sample, e := s.source.FetchSubscriptions(ctx, c.uid)
		if e != nil {
			failures = append(failures, errors.New("SUBSCRIPTION_FETCH_FAILED"))
			continue
		}
		if e = s.apply(ctx, c, sample); e != nil {
			failures = append(failures, e)
		}
	}
	key := "subscription_last_scan_success"
	if len(failures) > 0 {
		key = "subscription_scan_error"
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO metrics(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, key, s.now().Unix())
	return errors.Join(append(failures, e)...)
}
func (s *Scanner) apply(ctx context.Context, c candidate, sample domain.SubscriptionSnapshot) error {
	if e := Validate(sample, c.uid, s.now()); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var binding, rule, warning, critical int64
	var active, enabled bool
	e = tx.QueryRowContext(ctx, `SELECT b.version,p.version,b.active,p.enabled,p.warning_percent,p.critical_percent FROM bindings b JOIN subscription_preferences p ON p.user_id=b.user_id WHERE b.user_id=?`, c.uid).Scan(&binding, &rule, &active, &enabled, &warning, &critical)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if !active || !enabled || binding != c.binding || rule != c.rule {
		return nil
	}
	var last int64
	e = tx.QueryRowContext(ctx, `SELECT sampled_at FROM subscription_snapshots WHERE user_id=?`, c.uid).Scan(&last)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if sample.At.UnixNano() <= last {
		return errors.New("SUBSCRIPTION_OUT_OF_ORDER")
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO subscription_snapshots VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET sampled_at=excluded.sampled_at`, c.uid, sample.At.UnixNano())
	if e != nil {
		return e
	}
	// Cancel everything absent, inactive, expired, unlimited, reset or superseded by this full snapshot.
	rows, e := tx.QueryContext(ctx, `SELECT event_id,subscription_id,kind,window_start,limit_micros,binding_version,rule_version,level FROM subscription_outbox WHERE user_id=? AND status='pending'`, c.uid)
	if e != nil {
		return e
	}
	var cancelIDs []string
	for rows.Next() {
		var id, kind, ws string
		var subID, limit, bv, rv int64
		var level int
		if e = rows.Scan(&id, &subID, &kind, &ws, &limit, &bv, &rv, &level); e != nil {
			rows.Close()
			return e
		}
		valid := false
		for _, sub := range sample.Subscriptions {
			if sub.SubscriptionID != subID {
				continue
			}
			for _, q := range sub.Windows {
				if q.Kind == kind && eligible(sample, sub, q, s.now()) && start(q) == ws && *q.LimitMicros == limit && bv == binding && rv == rule && severity(q, warning, critical) == level {
					valid = true
				}
			}
		}
		if !valid {
			cancelIDs = append(cancelIDs, id)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range cancelIDs {
		if _, e = tx.ExecContext(ctx, `UPDATE subscription_outbox SET status='cancelled',last_error='SNAPSHOT_CHANGED' WHERE event_id=? AND status='pending'`, id); e != nil {
			return e
		}
	}
	for _, sub := range sample.Subscriptions {
		for _, q := range sub.Windows {
			if !eligible(sample, sub, q, s.now()) {
				continue
			}
			var ws string
			var limit, bv, rv, cycle int64
			var level int
			e = tx.QueryRowContext(ctx, `SELECT window_start,limit_micros,binding_version,rule_version,cycle,level FROM subscription_states WHERE user_id=? AND subscription_id=? AND kind=?`, c.uid, sub.SubscriptionID, q.Kind).Scan(&ws, &limit, &bv, &rv, &cycle, &level)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if ws != start(q) || bv != binding {
				cycle++
				level = 0
			}
			target := severity(q, warning, critical)
			if target == 0 && level > 0 {
				cycle++
				level = 0
			}
			// Rule/limit edits retain already-observed levels; they cannot resend the same severity.
			if target > level {
				var bytes [16]byte
				if _, e = rand.Read(bytes[:]); e != nil {
					return e
				}
				_, e = tx.ExecContext(ctx, `INSERT INTO subscription_outbox(event_id,user_id,subscription_id,kind,window_start,limit_micros,binding_version,rule_version,cycle,level,sampled_at,next_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, hex.EncodeToString(bytes[:]), c.uid, sub.SubscriptionID, q.Kind, start(q), *q.LimitMicros, binding, rule, cycle, target, sample.At.UnixNano(), s.now().Unix(), s.now().Unix())
				if e != nil {
					return e
				}
				level = target
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO subscription_states VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(user_id,subscription_id,kind) DO UPDATE SET window_start=excluded.window_start,limit_micros=excluded.limit_micros,binding_version=excluded.binding_version,rule_version=excluded.rule_version,cycle=excluded.cycle,level=excluded.level`, c.uid, sub.SubscriptionID, q.Kind, start(q), *q.LimitMicros, binding, rule, cycle, level)
			if e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}
