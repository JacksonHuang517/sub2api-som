package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"som/feishu/internal/domain"
	"som/feishu/internal/som"
	"som/feishu/internal/store"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "only-test-key-32-bytes-not-production-secret"

func setup(t *testing.T) *Server {
	t.Helper()
	db, e := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{DB: db, Feishu: &Mock{Origin: "http://127.0.0.1:5186"}, Cfg: Config{Origin: "http://127.0.0.1:5186", ProofKey: testKey, Enabled: true, Mock: true}}
}
func proof(uid int64, session string, role string) string {
	now := time.Now()
	return som.Sign(som.Claims{UserID: uid, Session: session, Role: role, Audience: "som-feishu", Issued: now.Unix(), Expires: now.Add(time.Minute).Unix()}, testKey)
}
func request(s *Server, method, path, token string, cookie *http.Cookie, data string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, prefix+path, strings.NewReader(data))
	r.Header.Set("Origin", s.Cfg.Origin)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func start(t *testing.T, s *Server, token string) (*http.Cookie, string) {
	t.Helper()
	w := request(s, "POST", "/binding/start", token, nil, "{}")
	if w.Code != 200 {
		t.Fatalf("start %d %s", w.Code, w.Body)
	}
	var b map[string]string
	json.Unmarshal(w.Body.Bytes(), &b)
	u, _ := url.Parse(b["authorize_url"])
	return w.Result().Cookies()[0], "/oauth/callback?" + u.RawQuery
}
func bind(t *testing.T, s *Server, token string) *http.Cookie {
	t.Helper()
	cookie, path := start(t, s, token)
	w := request(s, "GET", path, "", cookie, "")
	if w.Code != 303 || strings.Contains(w.Header().Get("Location"), "error") {
		t.Fatalf("callback %d %s", w.Code, w.Header().Get("Location"))
	}
	w = request(s, "POST", "/binding/confirm", token, cookie, "{}")
	if w.Code != 200 {
		t.Fatalf("confirm %d %s", w.Code, w.Body)
	}
	return cookie
}
func TestBindingPrivacyPreferencesUnbind(t *testing.T) {
	s := setup(t)
	token := proof(1, "session-a", "admin")
	cookie := bind(t, s, token)
	w := request(s, "GET", "/me", token, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "张三") {
		t.Fatal(w.Body)
	}
	for _, secret := range []string{"mock-open-id", "open_id", "proof", "secret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("private field leaked")
		}
	}
	w = request(s, "PUT", "/preferences", token, cookie, `{"enabled":true,"warning_micros":5000000,"critical_micros":1000000,"version":1}`)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	w = request(s, "PUT", "/preferences", token, cookie, `{"enabled":false,"warning_micros":5000000,"critical_micros":1000000,"version":1}`)
	if w.Code != 409 {
		t.Fatal("stale version accepted")
	}
	_, e := s.DB.Exec("INSERT INTO notification_outbox(event_id,user_id,binding_version,rule_version,cycle,level,balance_micros,threshold_micros,sampled_at,next_at,created_at) VALUES('e',1,1,2,1,1,1,5000000,1,1,1)")
	if e != nil {
		t.Fatal(e)
	}
	w = request(s, "DELETE", "/binding", token, cookie, "")
	if w.Code != 204 {
		t.Fatal(w.Body)
	}
	var status string
	s.DB.QueryRow("SELECT status FROM notification_outbox").Scan(&status)
	if status != "cancelled" {
		t.Fatal(status)
	}
	w = request(s, "GET", "/me", token, cookie, "")
	if !strings.Contains(w.Body.String(), `"binding":null`) {
		t.Fatal(w.Body)
	}
}
func TestAccessControlAndOrigin(t *testing.T) {
	s := setup(t)
	for _, token := range []string{"", "garbage", proof(1, "x", "admin") + "x"} {
		if w := request(s, "POST", "/binding/start", token, nil, "{}"); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("POST", prefix+"/binding/start", bytes.NewBufferString("{}"))
	r.Header.Set("Authorization", "Bearer "+proof(1, "a", "admin"))
	r.Header.Set("Origin", "https://evil.test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = request(s, "POST", "/binding/start", proof(1, "a", "user"), nil, `{"user_id":2,"role":"admin"}`); w.Code != 400 {
		t.Fatal("forged body accepted")
	}
	if w = request(s, "GET", "/admin/status", proof(1, "a", "user"), nil, ""); w.Code != 403 {
		t.Fatal("user read admin")
	}
}
func TestStateBrowserSessionAccountReplay(t *testing.T) {
	s := setup(t)
	token := proof(1, "original", "user")
	cookie, path := start(t, s, token)
	if w := request(s, "GET", path, "", nil, ""); !strings.Contains(w.Header().Get("Location"), "OAUTH_INVALID") {
		t.Fatal(w.Header())
	}
	w := request(s, "GET", path, "", cookie, "")
	if strings.Contains(w.Header().Get("Location"), "error") {
		t.Fatal(w.Header())
	}
	w = request(s, "GET", path, "", cookie, "")
	if !strings.Contains(w.Header().Get("Location"), "OAUTH_EXPIRED") {
		t.Fatal("replayed callback")
	}
	for _, other := range []string{proof(2, "original", "user"), proof(1, "different", "user")} {
		w = request(s, "POST", "/binding/confirm", other, cookie, "{}")
		if w.Code != 409 {
			t.Fatal("account/session switch allowed")
		}
	}
	w = request(s, "POST", "/binding/confirm", token, cookie, "{}")
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	w = request(s, "POST", "/binding/confirm", token, cookie, "{}")
	if w.Code != 409 {
		t.Fatal("confirm replay")
	}
}
func TestExpiredStateAndConflict(t *testing.T) {
	s := setup(t)
	token := proof(1, "a", "user")
	cookie, path := start(t, s, token)
	s.DB.Exec("UPDATE oauth_attempts SET expires_at=0")
	w := request(s, "GET", path, "", cookie, "")
	if !strings.Contains(w.Header().Get("Location"), "OAUTH_EXPIRED") {
		t.Fatal("expiry bypass")
	}
	bind(t, s, token)
	other := proof(2, "b", "user")
	cookie, path = start(t, s, other)
	request(s, "GET", path, "", cookie, "")
	w = request(s, "POST", "/binding/confirm", other, cookie, "{}")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "IDENTITY_ALREADY_BOUND") {
		t.Fatal(w.Body)
	}
}
func TestConcurrentCallbackSingleConsumption(t *testing.T) {
	s := setup(t)
	token := proof(1, "a", "user")
	cookie, path := start(t, s, token)
	var wg sync.WaitGroup
	codes := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- request(s, "GET", path, "", cookie, "").Header().Get("Location") }()
	}
	wg.Wait()
	close(codes)
	success := 0
	for v := range codes {
		if v == s.Cfg.Origin+"/profile" {
			success++
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
}
func TestProductionDisablesMockEndpoints(t *testing.T) {
	s := setup(t)
	s.Cfg.Mock = false
	w := request(s, "POST", "/mock/session", "", nil, "{}")
	if w.Code == 200 {
		t.Fatal("mock endpoint exposed")
	}
	cookie, _ := start(t, s, proof(1, "a", "user"))
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("insecure cookie")
	}
}
func TestThresholdValidation(t *testing.T) {
	s := setup(t)
	token := proof(1, "a", "user")
	bind(t, s, token)
	for _, body := range []string{`{"enabled":true,"warning_micros":1,"critical_micros":1,"version":1}`, `{"enabled":true,"warning_micros":1.1,"critical_micros":0,"version":1}`, `{"enabled":true,"warning_micros":1000000000001,"critical_micros":1,"version":1}`} {
		if w := request(s, "PUT", "/preferences", token, nil, body); w.Code != 400 {
			t.Fatal(w.Body)
		}
	}
}

type badIdentity struct{ Mock }

func (b *badIdentity) Exchange(context.Context, string) (domain.Identity, error) {
	return domain.Identity{}, nil
}
func TestEmptyIdentityRejected(t *testing.T) {
	s := setup(t)
	s.Feishu = &badIdentity{Mock{Origin: s.Cfg.Origin}}
	cookie, path := start(t, s, proof(1, "a", "user"))
	w := request(s, "GET", path, "", cookie, "")
	if !strings.Contains(w.Header().Get("Location"), "OAUTH_IDENTITY_REJECTED") {
		t.Fatal(w.Header())
	}
}
