// Package alerts implements durable balance threshold transitions.
package alerts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"som/feishu/internal/domain"
)

type Config struct {
	Now        func() time.Time
	MaxAge     time.Duration
	Hysteresis int64
}
type Scanner struct {
	db       *sql.DB
	balances domain.Balances
	cfg      Config
	mu       sync.Mutex
	cursor   int64
}

func New(db *sql.DB, balances domain.Balances, cfg Config) *Scanner {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 5 * time.Minute
	}
	if cfg.Hysteresis <= 0 {
		cfg.Hysteresis = 100000
	}
	return &Scanner{db: db, balances: balances, cfg: cfg}
}

type candidate struct{ id, binding, rule, warning, critical int64 }

func (s *Scanner) Scan(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT b.user_id,b.version,p.version,p.warning_micros,p.critical_micros FROM bindings b JOIN preferences p ON p.user_id=b.user_id WHERE b.active=1 AND p.enabled=1 AND b.user_id>? ORDER BY b.user_id LIMIT 100`, s.cursor)
	if err != nil {
		return err
	}
	var users []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.binding, &c.rule, &c.warning, &c.critical); err != nil {
			rows.Close()
			return err
		}
		users = append(users, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(users) < 100 {
		s.cursor = 0
	} else {
		s.cursor = users[len(users)-1].id
	}
	var failures []error
	for _, c := range users {
		sample, fetchErr := s.balances.Fetch(ctx, c.id)
		if fetchErr != nil {
			failures = append(failures, errors.New("BALANCE_FETCH_FAILED"))
			continue
		}
		if err = s.apply(ctx, c, sample); err != nil {
			failures = append(failures, err)
		}
	}
	key := "last_scan_success"
	if len(failures) > 0 {
		key = "scan_error"
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO metrics(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, key, s.cfg.Now().Unix())
	if err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
func (s *Scanner) apply(ctx context.Context, c candidate, sample domain.Snapshot) error {
	now := s.cfg.Now()
	if sample.UserID != c.id || sample.Currency != "USD" || sample.At.IsZero() || sample.At.After(now.Add(30*time.Second)) || now.Sub(sample.At) > s.cfg.MaxAge {
		return errors.New("BALANCE_INVALID_OR_STALE")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var binding, rule int64
	var enabled, active int
	err = tx.QueryRowContext(ctx, `SELECT b.version,p.version,b.active,p.enabled FROM bindings b JOIN preferences p ON p.user_id=b.user_id WHERE b.user_id=?`, c.id).Scan(&binding, &rule, &active, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if binding != c.binding || rule != c.rule || active != 1 || enabled != 1 {
		return nil
	}
	var cycle, last, previousRule int64
	var level, recovery int
	err = tx.QueryRowContext(ctx, `SELECT cycle,level,recovery_count,last_sample_at,rule_version FROM alert_states WHERE user_id=?`, c.id).Scan(&cycle, &level, &recovery, &last, &previousRule)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previousRule != c.rule {
		recovery = 0
	}
	var snapshotAt int64
	err = tx.QueryRowContext(ctx, `SELECT sampled_at FROM balance_snapshots WHERE user_id=?`, c.id).Scan(&snapshotAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if snapshotAt > last {
		last = snapshotAt
	}
	if sample.At.Unix() <= last {
		return errors.New("BALANCE_OUT_OF_ORDER")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO balance_snapshots(user_id,balance_micros,currency,sampled_at,active) VALUES(?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET balance_micros=excluded.balance_micros,currency=excluded.currency,sampled_at=excluded.sampled_at,active=excluded.active`, c.id, sample.Balance, sample.Currency, sample.At.Unix(), sample.Active)
	if err != nil {
		return err
	}
	if !sample.Active {
		_, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET status='cancelled',last_error='USER_INACTIVE' WHERE user_id=? AND status='pending'`, c.id)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET status='cancelled',last_error='VERSION_CHANGED' WHERE user_id=? AND status='pending' AND (rule_version<>? OR binding_version<>?)`, c.id, c.rule, c.binding)
	if err != nil {
		return err
	}
	target := 0
	threshold := c.warning
	switch {
	case sample.Balance <= 0:
		target = 3
		threshold = 0
	case sample.Balance <= c.critical:
		target = 2
		threshold = c.critical
	case sample.Balance <= c.warning:
		target = 1
	}
	if sample.Balance > c.warning+s.cfg.Hysteresis {
		recovery++
		if recovery >= 2 {
			if level > 0 {
				_, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET status='cancelled',last_error='CYCLE_RECOVERED' WHERE user_id=? AND status='pending'`, c.id)
				if err != nil {
					return err
				}
				cycle++
			}
			level = 0
			recovery = 0
		}
	} else {
		recovery = 0
	}
	if target > level {
		_, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET status='cancelled',last_error='LEVEL_SUPERSEDED' WHERE user_id=? AND status='pending' AND level<?`, c.id, target)
		if err != nil {
			return err
		}
		var bytes [16]byte
		if _, err = rand.Read(bytes[:]); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO notification_outbox(event_id,user_id,binding_version,rule_version,cycle,level,balance_micros,threshold_micros,sampled_at,next_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, hex.EncodeToString(bytes[:]), c.id, c.binding, c.rule, cycle, target, sample.Balance, threshold, sample.At.Unix(), now.Unix(), now.Unix())
		if err != nil {
			return err
		}
		level = target
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO alert_states(user_id,rule_version,cycle,level,recovery_count,last_sample_at) VALUES(?,?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET rule_version=excluded.rule_version,cycle=excluded.cycle,level=excluded.level,recovery_count=excluded.recovery_count,last_sample_at=excluded.last_sample_at`, c.id, c.rule, cycle, level, recovery, sample.At.Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}
