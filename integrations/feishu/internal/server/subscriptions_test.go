package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubscriptionPreferencesIndependentVersionedAndUnbind(t *testing.T) {
	s := setup(t)
	token := proof(1, "session", "admin")
	cookie := bind(t, s, token)
	w := request(s, "GET", "/subscription-preferences", token, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Body)
	}
	w = request(s, "PUT", "/subscription-preferences", token, cookie, `{"enabled":true,"warning_percent":80,"critical_percent":95,"version":1}`)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	w = request(s, "PUT", "/subscription-preferences", token, cookie, `{"enabled":true,"warning_percent":80,"critical_percent":95,"version":1}`)
	if w.Code != 409 {
		t.Fatal("stale accepted")
	}
	w = request(s, "PUT", "/subscription-preferences", token, cookie, `{"enabled":true,"warning_percent":80.5,"critical_percent":95,"version":2}`)
	if w.Code != 400 {
		t.Fatal("decimal accepted")
	}
	w = request(s, "PUT", "/preferences", token, cookie, `{"enabled":true,"warning_micros":5000000,"critical_micros":1000000,"version":1}`)
	if w.Code != 200 {
		t.Fatal("subscription changed wallet version", w.Body)
	}
	w = request(s, "GET", "/subscription-preferences", proof(2, "another", "user"), nil, "")
	if strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatal("cross-user leak")
	}
	request(s, "DELETE", "/binding", token, cookie, "")
	w = request(s, "GET", "/me", token, cookie, "")
	var me struct {
		SubscriptionPreferences struct {
			Enabled bool  `json:"enabled"`
			Version int64 `json:"version"`
		} `json:"subscription_preferences"`
	}
	json.Unmarshal(w.Body.Bytes(), &me)
	if me.SubscriptionPreferences.Enabled || me.SubscriptionPreferences.Version != 3 {
		t.Fatal(w.Body)
	}
}
func TestSubscriptionsRequireSessionOriginAndNeverReturnFakeZero(t *testing.T) {
	s := setup(t)
	token := proof(1, "session", "user")
	w := request(s, "GET", "/subscriptions", "", nil, "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = request(s, "GET", "/subscriptions", token, nil, "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "UNAVAILABLE") {
		t.Fatal(w.Body)
	}
	s.Subscriptions = &Mock{}
	w = request(s, "GET", "/subscriptions", token, nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user_id":1`) {
		t.Fatal(w.Body)
	}
	r := httptest.NewRequest("PUT", prefix+"/subscription-preferences", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://other.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("wrong origin accepted", w.Body)
	}
}
