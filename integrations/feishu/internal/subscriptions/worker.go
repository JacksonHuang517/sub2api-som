// Package subscriptions provides a conservative single-sender durable delivery queue.
package subscriptions

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
	source   domain.Subscriptions
	sender   domain.Feishu
	cfg      Config
	mu       sync.Mutex
	lastSend time.Time
}

func NewWorker(db *sql.DB, source domain.Subscriptions, sender domain.Feishu, cfg Config) *Worker {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Worker{db: db, source: source, sender: sender, cfg: cfg}
}

type event struct {
	id                                        string
	uid, subID, binding, rule, limit, sampled int64
	kind, windowStart                         string
	level, attempts                           int
}

func (w *Worker) Drain(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.db.ExecContext(ctx, `UPDATE subscription_outbox SET status='uncertain',last_error='LEASE_EXPIRED' WHERE status='leased' AND lease_until<=?`, w.cfg.Now().Unix())
	if err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		var e event
		err = w.db.QueryRowContext(ctx, `SELECT event_id,user_id,subscription_id,kind,window_start,limit_micros,binding_version,rule_version,level,sampled_at,attempts FROM subscription_outbox WHERE status='pending' AND next_at<=? ORDER BY created_at,event_id LIMIT 1`, w.cfg.Now().Unix()).Scan(&e.id, &e.uid, &e.subID, &e.kind, &e.windowStart, &e.limit, &e.binding, &e.rule, &e.level, &e.sampled, &e.attempts)
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
		result, err := w.db.ExecContext(ctx, `UPDATE subscription_outbox SET status='leased',lease_until=?,attempts=attempts+1 WHERE event_id=? AND status='pending'`, w.cfg.Now().Add(30*time.Second).Unix(), e.id)
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
		_, cleanupErr := w.db.ExecContext(cleanup, `UPDATE subscription_outbox SET status=?,last_error=?,next_at=?,lease_until=0 WHERE event_id=? AND status='leased'`, status, code, next, e.id)
		resultErr = errors.Join(resultErr, cleanupErr)
	}()

	// This transaction serializes delivery against unbinding and disabling. Its
	// lifetime, including the subscription request and send, is bounded by ten seconds.
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var err error
	tx, err = w.db.BeginTx(callCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	finish := func(status, code, id string, next int64) error {
		_, err := tx.ExecContext(callCtx, `UPDATE subscription_outbox SET status=?,last_error=?,message_id=?,next_at=?,lease_until=0 WHERE event_id=? AND status='leased'`, status, code, id, next, e.id)
		if err != nil {
			return err
		}
		if status == "failed" || status == "uncertain" {
			_, err = tx.ExecContext(callCtx, `INSERT INTO metrics(name,value) VALUES('subscription_send_error',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, w.cfg.Now().Unix())
			if err != nil {
				return err
			}
		}
		if status == "sent" {
			_, err = tx.ExecContext(callCtx, `INSERT INTO metrics(name,value) VALUES('subscription_last_send_success',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, w.cfg.Now().Unix())
			if err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	var openID, name string
	var binding, rule int64
	var active, enabled int
	var warning, critical int64
	err = tx.QueryRowContext(callCtx, `SELECT b.open_id,b.display_name,b.version,p.version,b.active,p.enabled,p.warning_percent,p.critical_percent FROM bindings b JOIN subscription_preferences p ON p.user_id=b.user_id WHERE b.user_id=?`, e.uid).Scan(&openID, &name, &binding, &rule, &active, &enabled, &warning, &critical)
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
	err = tx.QueryRowContext(callCtx, `SELECT sampled_at FROM subscription_snapshots WHERE user_id=?`, e.uid).Scan(&latestSample)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	sample, err := w.source.FetchSubscriptions(callCtx, e.uid)
	if err != nil {
		status, code, next := w.retryPlan(e, "SUBSCRIPTION_FETCH_RETRY", 0)
		return finish(status, code, "", next)
	}
	if Validate(sample, e.uid, w.cfg.Now()) != nil || sample.At.UnixNano() < e.sampled || sample.At.UnixNano() < latestSample {
		status, code, next := w.retryPlan(e, "SUBSCRIPTION_INVALID_OR_STALE", 0)
		return finish(status, code, "", next)
	}
	var current *domain.SubscriptionMessage
	for _, sub := range sample.Subscriptions {
		if sub.SubscriptionID != e.subID {
			continue
		}
		for _, q := range sub.Windows {
			if q.Kind == e.kind && eligible(sample, sub, q, w.cfg.Now()) && start(q) == e.windowStart && *q.LimitMicros == e.limit && severity(q, warning, critical) == e.level {
				percent := warning
				if e.level == 2 {
					percent = critical
				}
				if e.level == 3 {
					percent = 100
				}
				current = &domain.SubscriptionMessage{SubscriptionID: sub.SubscriptionID, GroupName: sub.GroupName, Window: q, ExpiresAt: sub.ExpiresAt, Percent: percent}
			}
		}
	}
	if current == nil {
		return finish("cancelled", "SUBSCRIPTION_CHANGED", "", 0)
	}
	if !w.cfg.SendingEnabled {
		return finish("dry_run", "", "", 0)
	}
	if err := callCtx.Err(); err != nil {
		return err
	}
	sendStarted = true
	w.lastSend = time.Now()
	id, sendErr := w.sender.Send(callCtx, domain.Message{EventID: e.id, OpenID: openID, DisplayName: name, Level: e.level, Subscription: current, At: sample.At, SiteURL: w.cfg.SiteURL})
	if callCtx.Err() != nil { // The transaction may have rolled back; finalize outside it.
		tx.Rollback()
		cleanup, c := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer c()
		_, err = w.db.ExecContext(cleanup, `UPDATE subscription_outbox SET status='uncertain',last_error='SEND_TIMEOUT',lease_until=0 WHERE event_id=? AND status='leased'`, e.id)
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
