package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"som/feishu/internal/domain"
	"som/feishu/internal/som"
	"strings"
	"time"
)

const prefix = "/integrations/feishu/api"
const cookieName = "som_feishu_browser"

type Config struct {
	Origin, ProofKey string
	Enabled, Mock    bool
	Now              func() time.Time
}
type Server struct {
	Subscriptions domain.Subscriptions
	DB            *sql.DB
	Feishu        domain.Feishu
	Cfg           Config
	Tick          func(context.Context) error
	MockBalance   func(int64)
	MockRead      func() int64
}

func (s *Server) now() time.Time {
	if s.Cfg.Now != nil {
		return s.Cfg.Now()
	}
	return time.Now()
}
func hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func fail(w http.ResponseWriter, status int, code string) {
	reply(w, status, map[string]string{"error": code})
}
func body(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return false
	}
	return d.Decode(&struct{}{}) == io.EOF
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.Path == prefix+"/healthz" && r.Method == "GET" {
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == prefix+"/readyz" && r.Method == "GET" {
		if s.DB.PingContext(r.Context()) != nil {
			fail(w, 503, "NOT_READY")
		} else {
			reply(w, 200, map[string]bool{"ok": true})
		}
		return
	}
	if r.URL.Path == prefix+"/config" && r.Method == "GET" {
		reply(w, 200, map[string]bool{"enabled": s.Cfg.Enabled})
		return
	}
	if !strings.HasPrefix(r.URL.Path, prefix+"/") || !s.Cfg.Enabled {
		fail(w, 404, "DISABLED")
		return
	}
	if r.Method != "GET" {
		if r.Header.Get("Origin") != s.Cfg.Origin {
			fail(w, 403, "ORIGIN_REJECTED")
			return
		}
		if r.Method != "DELETE" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			fail(w, 415, "JSON_REQUIRED")
			return
		}
	}
	if path == "/oauth/callback" && r.Method == "GET" {
		s.callback(w, r)
		return
	}
	if s.Cfg.Mock && path == "/mock/session" && r.Method == "POST" {
		c := som.Claims{UserID: 1, Role: "admin", Session: "mock-browser-session", Audience: "som-feishu", Issued: s.now().Unix(), Expires: s.now().Add(time.Minute).Unix()}
		reply(w, 200, map[string]string{"proof": som.Sign(c, s.Cfg.ProofKey)})
		return
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		fail(w, 401, "SESSION_REQUIRED")
		return
	}
	claims, e := som.Verify(strings.TrimPrefix(header, "Bearer "), s.Cfg.ProofKey, s.now())
	if e != nil {
		fail(w, 401, "SESSION_EXPIRED")
		return
	}
	switch {
	case path == "/me" && r.Method == "GET":
		s.me(w, r, claims)
	case path == "/binding/start" && r.Method == "POST":
		s.start(w, r, claims)
	case path == "/binding/confirm" && r.Method == "POST":
		s.confirm(w, r, claims)
	case path == "/binding" && r.Method == "DELETE":
		s.unbind(w, r, claims)
	case path == "/subscription-preferences" && (r.Method == "PUT" || r.Method == "GET"):
		s.subscriptionPreferences(w, r, claims)
	case path == "/subscriptions" && r.Method == "GET":
		s.subscriptionSnapshot(w, r, claims)
	case path == "/preferences" && r.Method == "PUT":
		s.preferences(w, r, claims)
	case path == "/admin/status" && r.Method == "GET":
		if claims.Role != "admin" {
			fail(w, 403, "ADMIN_REQUIRED")
			return
		}
		s.status(w, r)
	case s.Cfg.Mock && path == "/mock/balance" && r.Method == "PUT":
		var b struct {
			Balance int64 `json:"balance_micros"`
		}
		if !body(w, r, &b) || b.Balance < -1000000000000 || b.Balance > 1000000000000 {
			fail(w, 400, "INVALID_BALANCE")
			return
		}
		s.MockBalance(b.Balance)
		reply(w, 200, b)
	case s.Cfg.Mock && path == "/mock/tick" && r.Method == "POST":
		if e := s.Tick(r.Context()); e != nil {
			fail(w, 503, "TICK_FAILED")
			return
		}
		s.status(w, r)
	default:
		fail(w, 404, "NOT_FOUND")
	}
}
func browser(r *http.Request) string {
	c, e := r.Cookie(cookieName)
	if e != nil || len(c.Value) != 64 {
		return ""
	}
	return hash(c.Value)
}
func (s *Server) me(w http.ResponseWriter, r *http.Request, c som.Claims) {
	var b domain.Binding
	var at int64
	var binding any
	err := s.DB.QueryRowContext(r.Context(), "SELECT display_name,tenant_key,version,synced_at FROM bindings WHERE user_id=? AND active=1", c.UserID).Scan(&b.DisplayName, &b.TenantKey, &b.Version, &at)
	if err == nil {
		binding = map[string]any{"display_name": b.DisplayName, "tenant_key": b.TenantKey, "version": b.Version, "synced_at": time.Unix(at, 0).UTC()}
	} else if !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	p := domain.Preferences{Warning: 5000000, Critical: 1000000, Version: 1}
	err = s.DB.QueryRowContext(r.Context(), "SELECT enabled,warning_micros,critical_micros,version FROM preferences WHERE user_id=?", c.UserID).Scan(&p.Enabled, &p.Warning, &p.Critical, &p.Version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	var name, tenant string
	var pending any
	err = s.DB.QueryRowContext(r.Context(), "SELECT display_name,tenant_key FROM oauth_attempts WHERE user_id=? AND session_hash=? AND browser_hash=? AND consumed=2 AND expires_at>? ORDER BY expires_at DESC LIMIT 1", c.UserID, hash(c.Session), browser(r), s.now().Unix()).Scan(&name, &tenant)
	if err == nil {
		pending = map[string]string{"display_name": name, "tenant_key": tenant}
	}
	sp, err := s.subscriptionPreference(r, c.UserID)
	if err != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	reply(w, 200, map[string]any{"subscription_preferences": sp, "user_id": c.UserID, "binding": binding, "preferences": p, "pending": pending})
}
func (s *Server) start(w http.ResponseWriter, r *http.Request, c som.Claims) {
	var v struct{}
	if !body(w, r, &v) {
		fail(w, 400, "INVALID_REQUEST")
		return
	}
	state, bc := random(), random()
	tx, e := s.DB.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("DELETE FROM oauth_attempts WHERE expires_at<=? OR user_id=?", s.now().Unix(), c.UserID)
	if e == nil {
		_, e = tx.Exec("INSERT INTO oauth_attempts(state_hash,user_id,session_hash,browser_hash,expires_at) VALUES(?,?,?,?,?)", hash(state), c.UserID, hash(c.Session), hash(bc), s.now().Add(5*time.Minute).Unix())
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: bc, Path: prefix, MaxAge: 300, HttpOnly: true, Secure: !s.Cfg.Mock, SameSite: http.SameSiteLaxMode})
	url := s.Feishu.AuthorizeURL(state)
	reply(w, 200, map[string]string{"authorize_url": url})
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	bad := func(code string) { http.Redirect(w, r, s.Cfg.Origin+"/profile?feishu_error="+code, 303) }
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	bh := browser(r)
	if len(state) != 64 || code == "" || len(code) > 4096 || bh == "" {
		bad("OAUTH_INVALID")
		return
	}
	result, e := s.DB.ExecContext(r.Context(), "UPDATE oauth_attempts SET consumed=1 WHERE state_hash=? AND browser_hash=? AND consumed=0 AND expires_at>?", hash(state), bh, s.now().Unix())
	if e != nil {
		bad("STORAGE_ERROR")
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		bad("OAUTH_EXPIRED")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id, e := s.Feishu.Exchange(ctx, code)
	if e != nil || id.OpenID == "" || id.TenantKey == "" || id.AppID == "" || id.DisplayName == "" {
		bad("OAUTH_IDENTITY_REJECTED")
		return
	}
	result, e = s.DB.ExecContext(ctx, "UPDATE oauth_attempts SET consumed=2,tenant_key=?,app_id=?,open_id=?,display_name=? WHERE state_hash=? AND consumed=1 AND expires_at>?", id.TenantKey, id.AppID, id.OpenID, id.DisplayName, hash(state), s.now().Unix())
	if e != nil {
		bad("STORAGE_ERROR")
		return
	}
	n, _ = result.RowsAffected()
	if n != 1 {
		bad("OAUTH_EXPIRED")
		return
	}
	http.Redirect(w, r, s.Cfg.Origin+"/profile", 303)
}
func (s *Server) confirm(w http.ResponseWriter, r *http.Request, c som.Claims) {
	var v struct{}
	if !body(w, r, &v) {
		fail(w, 400, "INVALID_REQUEST")
		return
	}
	tx, e := s.DB.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	defer tx.Rollback()
	var state, tenant, app, open, name string
	e = tx.QueryRow("SELECT state_hash,tenant_key,app_id,open_id,display_name FROM oauth_attempts WHERE user_id=? AND session_hash=? AND browser_hash=? AND consumed=2 AND expires_at>? LIMIT 1", c.UserID, hash(c.Session), browser(r), s.now().Unix()).Scan(&state, &tenant, &app, &open, &name)
	if e != nil {
		fail(w, 409, "CONFIRMATION_EXPIRED")
		return
	}
	var owner int64
	e = tx.QueryRow("SELECT user_id FROM bindings WHERE tenant_key=? AND app_id=? AND open_id=? AND active=1", tenant, app, open).Scan(&owner)
	if e == nil && owner != c.UserID {
		fail(w, 409, "IDENTITY_ALREADY_BOUND")
		return
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	_, e = tx.Exec("UPDATE subscription_preferences SET enabled=0,version=version+1 WHERE user_id=? AND EXISTS(SELECT 1 FROM bindings WHERE user_id=? AND active=1 AND (tenant_key<>? OR app_id<>? OR open_id<>?))", c.UserID, c.UserID, tenant, app, open)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	_, e = tx.Exec("INSERT INTO bindings(user_id,tenant_key,app_id,open_id,display_name,version,synced_at,active) VALUES(?,?,?,?,?,1,?,1) ON CONFLICT(user_id) DO UPDATE SET tenant_key=excluded.tenant_key,app_id=excluded.app_id,open_id=excluded.open_id,display_name=excluded.display_name,version=bindings.version+1,synced_at=excluded.synced_at,active=1", c.UserID, tenant, app, open, name, s.now().Unix())
	if e == nil {
		_, e = tx.Exec("INSERT OR IGNORE INTO preferences(user_id) VALUES(?)", c.UserID)
	}
	if e == nil {
		_, e = tx.Exec("DELETE FROM oauth_attempts WHERE user_id=?", c.UserID)
	}
	if e == nil {
		_, e = tx.Exec("UPDATE notification_outbox SET status='cancelled' WHERE user_id=? AND status IN ('pending','leased')", c.UserID)
	}
	if e == nil {
		_, e = tx.Exec("UPDATE subscription_outbox SET status='cancelled',last_error='BINDING_CHANGED' WHERE user_id=? AND status IN ('pending','leased')", c.UserID)
	}
	if e == nil {
		_, e = tx.Exec("INSERT INTO audit_events(user_id,action,at) VALUES(?,'binding_confirm',?)", c.UserID, s.now().Unix())
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) unbind(w http.ResponseWriter, r *http.Request, c som.Claims) {
	tx, e := s.DB.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	defer tx.Rollback()
	for _, q := range []string{"UPDATE bindings SET active=0,version=version+1,open_id='',display_name='' WHERE user_id=?", "UPDATE preferences SET enabled=0,version=version+1 WHERE user_id=?", "DELETE FROM oauth_attempts WHERE user_id=?", "UPDATE notification_outbox SET status='cancelled' WHERE user_id=? AND status IN ('pending','leased')", "UPDATE subscription_preferences SET enabled=0,version=version+1 WHERE user_id=?", "UPDATE subscription_outbox SET status='cancelled',last_error='UNBOUND' WHERE user_id=? AND status IN ('pending','leased')"} {
		if _, e = tx.Exec(q, c.UserID); e != nil {
			fail(w, 500, "STORAGE_ERROR")
			return
		}
	}
	_, e = tx.Exec("INSERT INTO audit_events(user_id,action,at) VALUES(?,'binding_remove',?)", c.UserID, s.now().Unix())
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	reply(w, 204, nil)
}
func (s *Server) preferences(w http.ResponseWriter, r *http.Request, c som.Claims) {
	var p domain.Preferences
	if !body(w, r, &p) || p.Critical <= 0 || p.Warning <= p.Critical || p.Warning > 1000000000000 || p.Version <= 0 {
		fail(w, 400, "INVALID_THRESHOLDS")
		return
	}
	tx, e := s.DB.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	defer tx.Rollback()
	result, e := tx.Exec("UPDATE preferences SET enabled=?,warning_micros=?,critical_micros=?,version=version+1 WHERE user_id=? AND version=? AND EXISTS(SELECT 1 FROM bindings WHERE user_id=? AND active=1)", p.Enabled, p.Warning, p.Critical, c.UserID, p.Version, c.UserID)
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		fail(w, 409, "PREFERENCES_CONFLICT")
		return
	}
	_, e = tx.Exec("UPDATE notification_outbox SET status='cancelled' WHERE user_id=? AND status IN ('pending','leased')", c.UserID)
	if e == nil {
		_, e = tx.Exec("INSERT INTO audit_events(user_id,action,at) VALUES(?,'preferences_update',?)", c.UserID, s.now().Unix())
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	p.Version++
	reply(w, 200, p)
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int64{}
	rows, e := s.DB.QueryContext(r.Context(), "SELECT status,COUNT(*) FROM notification_outbox GROUP BY status")
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	for rows.Next() {
		var k string
		var n int64
		if rows.Scan(&k, &n) == nil {
			counts[k] = n
		}
	}
	rows.Close()
	metrics := map[string]int64{}
	rows, e = s.DB.QueryContext(r.Context(), "SELECT name,value FROM metrics")
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	for rows.Next() {
		var k string
		var v int64
		if rows.Scan(&k, &v) == nil {
			metrics[k] = v
		}
	}
	rows.Close()
	failures := map[string]int64{}
	rows, e = s.DB.QueryContext(r.Context(), "SELECT last_error,COUNT(*) FROM notification_outbox WHERE last_error!='' GROUP BY last_error")
	if e == nil {
		for rows.Next() {
			var k string
			var n int64
			if rows.Scan(&k, &n) == nil {
				failures[k] = n
			}
		}
		rows.Close()
	}
	var activeBindings, oldestPending, latestSnapshot int64
	s.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM bindings WHERE active=1").Scan(&activeBindings)
	s.DB.QueryRowContext(r.Context(), "SELECT COALESCE(MIN(created_at),0) FROM notification_outbox WHERE status='pending'").Scan(&oldestPending)
	s.DB.QueryRowContext(r.Context(), "SELECT COALESCE(MAX(sampled_at),0) FROM balance_snapshots").Scan(&latestSnapshot)
	subscriptionCounts := map[string]int64{}
	rows, e = s.DB.QueryContext(r.Context(), "SELECT status,COUNT(*) FROM subscription_outbox GROUP BY status")
	if e != nil {
		fail(w, 500, "STORAGE_ERROR")
		return
	}
	for rows.Next() {
		var k string
		var n int64
		if rows.Scan(&k, &n) == nil {
			subscriptionCounts[k] = n
		}
	}
	rows.Close()
	reply(w, 200, map[string]any{"subscription_outbox": subscriptionCounts, "outbox": counts, "metrics": metrics, "failures": failures, "mock": s.Cfg.Mock, "active_bindings": activeBindings, "oldest_pending_at": oldestPending, "latest_snapshot_at": latestSnapshot})
}
