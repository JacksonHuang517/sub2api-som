package server

import (
	"database/sql"
	"errors"
	"net/http"
	"som/feishu/internal/domain"
	"som/feishu/internal/som"
	"som/feishu/internal/subscriptions"
)

func (s *Server) subscriptionPreference(r *http.Request, id int64) (domain.SubscriptionPreferences, error) {
	p := domain.SubscriptionPreferences{Warning: 80, Critical: 95, Version: 1}
	e := s.DB.QueryRowContext(r.Context(), `SELECT enabled,warning_percent,critical_percent,version FROM subscription_preferences WHERE user_id=?`, id).Scan(&p.Enabled, &p.Warning, &p.Critical, &p.Version)
	if errors.Is(e, sql.ErrNoRows) {
		e = nil
	}
	return p, e
}
func (s *Server) subscriptionPreferences(w http.ResponseWriter, r *http.Request, c som.Claims) {
	if r.Method == "GET" {
		p, e := s.subscriptionPreference(r, c.UserID)
		if e != nil {
			fail(w, 500, "STORAGE_ERROR")
			return
		}
		reply(w, 200, p)
		return
	}
	var p domain.SubscriptionPreferences
	if !body(w, r, &p) || p.Warning <= 0 || p.Warning >= p.Critical || p.Critical >= 100 || p.Version <= 0 {
		fail(w, 400, "INVALID_SUBSCRIPTION_THRESHOLDS")
		return
	}
	tx, e := s.DB.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO subscription_preferences(user_id) VALUES(?)`, c.UserID)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	result, e := tx.ExecContext(r.Context(), `UPDATE subscription_preferences SET enabled=?,warning_percent=?,critical_percent=?,version=version+1 WHERE user_id=? AND version=? AND EXISTS(SELECT 1 FROM bindings WHERE user_id=? AND active=1)`, p.Enabled, p.Warning, p.Critical, c.UserID, p.Version, c.UserID)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	n, e := result.RowsAffected()
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	if n != 1 {
		fail(w, 409, "PREFERENCES_CONFLICT")
		return
	}
	_, e = tx.ExecContext(r.Context(), `UPDATE subscription_outbox SET status='cancelled',last_error='RULE_CHANGED' WHERE user_id=? AND status IN ('pending','leased')`, c.UserID)
	if e == nil {
		_, e = tx.ExecContext(r.Context(), `INSERT INTO audit_events(user_id,action,at) VALUES(?,'subscription_preferences_update',?)`, c.UserID, s.now().Unix())
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	p.Version++
	reply(w, 200, p)
}
func (s *Server) subscriptionSnapshot(w http.ResponseWriter, r *http.Request, c som.Claims) {
	if s.Subscriptions == nil {
		fail(w, 503, "SUBSCRIPTIONS_UNAVAILABLE")
		return
	}
	sample, e := s.Subscriptions.FetchSubscriptions(r.Context(), c.UserID)
	if e != nil || subscriptions.Validate(sample, c.UserID, s.now()) != nil {
		fail(w, 503, "SUBSCRIPTIONS_UNAVAILABLE")
		return
	}
	reply(w, 200, sample)
}
