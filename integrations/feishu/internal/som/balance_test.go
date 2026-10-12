package som

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMinimalBalanceAdapter(t *testing.T) {
	body := fmt.Sprintf(`{"code":0,"data":{"user_id":1,"balance_micros":4800000,"currency":"USD","active":true,"at":%q}}`, time.Now().UTC().Format(time.RFC3339))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/feishu/balance/1" || r.Header.Get("Authorization") != "Bearer test-key-more-than-32-bytes-long-123" {
			t.Error("invalid request")
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c, e := New(srv.URL, "test-key-more-than-32-bytes-long-123")
	if e != nil {
		t.Fatal(e)
	}
	v, e := c.Fetch(context.Background(), 1)
	if e != nil || v.Balance != 4800000 {
		t.Fatal(e, v)
	}
	for _, amount := range []string{"", `"balance_micros":null,`} {
		body = fmt.Sprintf(`{"code":0,"data":{"user_id":1,%s"currency":"USD","active":true,"at":%q}}`, amount, time.Now().UTC().Format(time.RFC3339))
		if _, e = c.Fetch(context.Background(), 1); e == nil {
			t.Fatal("missing/null amount accepted as zero")
		}
	}
	body = `{"code":0,"data":{"user_id":2,"currency":"USD"}}`
	if _, e = c.Fetch(context.Background(), 1); e == nil {
		t.Fatal("accepted wrong user")
	}
}
