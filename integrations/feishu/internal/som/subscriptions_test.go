package som

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionReadProtocolAndMissingFields(t *testing.T) {
	now := time.Now().UTC()
	data := map[string]any{"user_id": int64(7), "active": true, "currency": "USD", "at": now, "subscriptions": []any{map[string]any{"subscription_id": 10, "group_id": 2, "group_name": "quota", "status": "active", "expires_at": now.Add(time.Hour), "windows": []any{map[string]any{"kind": "daily", "limited": true, "limit_micros": 100, "used_micros": 80, "remaining_micros": 20, "window_start": now.Add(-time.Hour)}, map[string]any{"kind": "weekly", "limited": false, "used_micros": 0}, map[string]any{"kind": "monthly", "limited": false, "used_micros": 0}}}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/feishu/subscriptions/7" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
			t.Error("wrong read route/key")
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	defer srv.Close()
	c, e := New(srv.URL, strings.Repeat("x", 32))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.FetchSubscriptions(context.Background(), 7); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"active", "at", "user_id", "subscriptions"} {
		value := data[field]
		delete(data, field)
		if _, e = c.FetchSubscriptions(context.Background(), 7); e == nil {
			t.Fatal("missing accepted", field)
		}
		data[field] = value
	}
	sub := data["subscriptions"].([]any)[0].(map[string]any)
	q := sub["windows"].([]any)[0].(map[string]any)
	for _, field := range []string{"used_micros", "limited", "limit_micros", "remaining_micros"} {
		value := q[field]
		delete(q, field)
		if _, e = c.FetchSubscriptions(context.Background(), 7); e == nil {
			t.Fatal("missing quota accepted", field)
		}
		q[field] = value
	}
	data["user_id"] = 8
	if _, e = c.FetchSubscriptions(context.Background(), 7); e == nil {
		t.Fatal("cross-user accepted")
	}
}
