package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"som/feishu/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionMessageUsesPlainTextAndFixedLinks(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			w.Write([]byte(`{"code":0,"tenant_access_token":"test-token","expire":7200}`))
			return
		}
		var b map[string]string
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Fatal(e)
		}
		var content map[string]string
		if e := json.Unmarshal([]byte(b["content"]), &content); e != nil {
			t.Fatal(e)
		}
		if b["msg_type"] != "text" || b["receive_id"] != "ou_test" || b["uuid"] != "subscription-event" || len(content) != 1 {
			t.Error("subscription protocol changed")
		}
		for _, want := range []string{"订阅用量警戒", "＜at user_id=\"all\"＞unsafe＜/at＞ **group**", "https://som.example/subscriptions", "https://som.example/profile", "80%", "每日", "本窗口到期前不再刷新"} {
			if !strings.Contains(content["text"], want) {
				t.Error("missing plain text", want)
			}
		}
		w.Write([]byte(`{"code":0,"data":{"message_id":"subscription-message"}}`))
	})
	limit := int64(100000000)
	rem := int64(19000000)
	id, e := c.Send(context.Background(), domain.Message{EventID: "subscription-event", OpenID: "ou_test", Level: 1, At: time.Now(), Subscription: &domain.SubscriptionMessage{SubscriptionID: 10, GroupName: `<at user_id="all">unsafe</at> **group**`, Window: domain.QuotaWindow{Kind: "daily", LimitMicros: &limit, UsedMicros: 81000000, RemainingMicros: &rem}, ExpiresAt: time.Now().Add(time.Hour), Percent: 80}})
	if e != nil || id != "subscription-message" {
		t.Fatal(id, e)
	}
}
