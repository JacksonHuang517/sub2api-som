// Package outbox provides a conservative single-sender durable delivery queue.
package outbox

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"som/feishu/internal/domain"
)

type Config struct {
	Now            func() time.Time
	SendingEnabled bool
	AllowedUsers   map[int64]bool
	SiteURL        string
}
type Worker struct {
	db       *sql.DB
	balances domain.Balances
	sender   domain.Feishu
	cfg      Config
	mu       sync.Mutex
	lastSend time.Time
}

func New(db *sql.DB, balances domain.Balances, sender domain.Feishu, cfg Config) *Worker {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Worker{db: db, balances: balances, sender: sender, cfg: cfg}
}

type event struct {
	id                                              string
	uid, binding, rule, balance, threshold, sampled int64
	level, attempts                                 int
}

func (w *Worker) Drain(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.db.ExecContext(ctx, `UPDATE notification_outbox SET status='uncertain',last_error='LEASE_EXPIRED' WHERE status='leased' AND lease_until<=?`, w.cfg.Now().Unix())
	if err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		var e event
		err = w.db.QueryRowContext(ctx, `SELECT event_id,user_id,binding_version,rule_version,level,balance_micros,threshold_micros,sampled_at,attempts FROM notification_outbox WHERE status='pending' AND next_at<=? ORDER BY created_at,event_id LIMIT 1`, w.cfg.Now().Unix()).Scan(&e.id, &e.uid, &e.binding, &e.rule, &e.level, &e.balance, &e.threshold, &e.sampled, &e.attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if w.cfg.SendingEnabled {
			if wait := time.Until(w.lastSend.Add(time.Second)); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		// Persist the lease before any external call. A crash can only become uncertain.
		result, err := w.db.ExecContext(ctx, `UPDATE notification_outbox SET status='leased',lease_until=?,attempts=attempts+1 WHERE event_id=? AND status='pending'`, w.cfg.Now().Add(30*time.Second).Unix(), e.id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		e.attempts++
		if err = w.deliver(ctx, e); err != nil {
			return err
		}
	}
	return nil
}
func (w *Worker) deliver(ctx context.Context, e event) (resultErr error) {
	var tx *sql.Tx
	sendStarted := false
	// Failures before invoking Send are known not to have been delivered, even
	// if the validation transaction was cancelled. Restore their bounded retry.
	defer func() {
		if resultErr == nil || sendStarted {
			return
		}
		if tx != nil {
			_ = tx.Rollback()
		}
		status, code, next := w.retryPlan(e, "VALIDATION_RETRY", 0)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, cleanupErr := w.db.ExecContext(cleanup, `UPDATE notification_outbox SET status=?,last_error=?,next_at=?,lease_until=0 WHERE event_id=? AND status='leased'`, status, code, next, e.id)
		resultErr = errors.Join(resultErr, cleanupErr)
	}()

	// This transaction serializes delivery against unbinding and disabling. Its
	// lifetime, including the balance request and send, is bounded by ten seconds.
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var err error
	tx, err = w.db.BeginTx(callCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	finish := func(status, code, id string, next int64) error {
		_, err := tx.ExecContext(callCtx, `UPDATE notification_outbox SET status=?,last_error=?,message_id=?,next_at=?,lease_until=0 WHERE event_id=? AND status='leased'`, status, code, id, next, e.id)
		if err != nil {
			return err
		}
		if status == "failed" || status == "uncertain" {
			_, err = tx.ExecContext(callCtx, `INSERT INTO metrics(name,value) VALUES('send_error',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, w.cfg.Now().Unix())
			if err != nil {
				return err
			}
		}
		if status == "sent" {
			_, err = tx.ExecContext(callCtx, `INSERT INTO metrics(name,value) VALUES('last_send_success',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, w.cfg.Now().Unix())
			if err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	var openID, name string
	var binding, rule int64
	var active, enabled int
	err = tx.QueryRowContext(callCtx, `SELECT b.open_id,b.display_name,b.version,p.version,b.active,p.enabled FROM bindings b JOIN preferences p ON p.user_id=b.user_id WHERE b.user_id=?`, e.uid).Scan(&openID, &name, &binding, &rule, &active, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return finish("cancelled", "BINDING_MISSING", "", 0)
	}
	if err != nil {
		return err
	}
	if binding != e.binding || rule != e.rule || active != 1 || enabled != 1 {
		return finish("cancelled", "BINDING_OR_RULE_CHANGED", "", 0)
	}
	if w.cfg.SendingEnabled && !w.cfg.AllowedUsers[e.uid] {
		return finish("cancelled", "USER_NOT_ALLOWED", "", 0)
	}
	var latestSample int64
	err = tx.QueryRowContext(callCtx, `SELECT sampled_at FROM balance_snapshots WHERE user_id=?`, e.uid).Scan(&latestSample)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	sample, err := w.balances.Fetch(callCtx, e.uid)
	if err != nil {
		status, code, next := w.retryPlan(e, "BALANCE_FETCH_RETRY", 0)
		return finish(status, code, "", next)
	}
	now := w.cfg.Now()
	if sample.UserID != e.uid || sample.Currency != "USD" || sample.At.IsZero() || sample.At.Unix() < e.sampled || sample.At.Unix() < latestSample || sample.At.After(now.Add(30*time.Second)) || now.Sub(sample.At) > 5*time.Minute {
		status, code, next := w.retryPlan(e, "BALANCE_INVALID_OR_STALE", 0)
		return finish(status, code, "", next)
	}
	if !sample.Active {
		return finish("cancelled", "USER_INACTIVE", "", 0)
	}
	if sample.Balance > e.threshold {
		return finish("cancelled", "BALANCE_RECOVERED", "", 0)
	}
	if !w.cfg.SendingEnabled {
		return finish("dry_run", "", "", 0)
	}
	if err := callCtx.Err(); err != nil {
		return err
	}
	sendStarted = true
	w.lastSend = time.Now()
	id, sendErr := w.sender.Send(callCtx, domain.Message{EventID: e.id, OpenID: openID, DisplayName: name, Level: e.level, Balance: sample.Balance, Threshold: e.threshold, At: sample.At, SiteURL: w.cfg.SiteURL})
	if callCtx.Err() != nil { // The transaction may have rolled back; finalize outside it.
		tx.Rollback()
		cleanup, c := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer c()
		_, err = w.db.ExecContext(cleanup, `UPDATE notification_outbox SET status='uncertain',last_error='SEND_TIMEOUT',lease_until=0 WHERE event_id=? AND status='leased'`, e.id)
		return err
	}
	if sendErr == nil {
		if id == "" {
			return finish("uncertain", "MISSING_MESSAGE_ID", "", 0)
		}
		return finish("sent", "", id, 0)
	}
	var se *domain.SendError
	if !errors.As(sendErr, &se) {
		return finish("uncertain", "SEND_UNCERTAIN", "", 0)
	}
	switch se.Kind {
	case "rate_limit", "transient", "token":
		status, code, next := w.retryPlan(e, "SEND_RETRY", se.RetryAfter)
		return finish(status, code, "", next)
	case "permission", "recipient", "protocol":
		return finish("failed", "SEND_"+se.Kind, "", 0)
	default:
		return finish("uncertain", "SEND_UNCERTAIN", "", 0)
	}
}

func (w *Worker) retryPlan(e event, code string, retryAfter time.Duration) (string, string, int64) {
	if e.attempts >= 5 {
		return "failed", "RETRY_EXHAUSTED", 0
	}
	delay := time.Duration(1<<e.attempts)*time.Second + time.Duration(rand.IntN(1000))*time.Millisecond
	if retryAfter > delay {
		delay = retryAfter
	}
	return "pending", code, w.cfg.Now().Add(delay).Unix() + 1
}
