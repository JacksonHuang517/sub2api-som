package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"som/feishu/internal/domain"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, e := New(Config{AppID: "cli_test", AppSecret: "dummy-secret", TenantKey: "tenant-test", RedirectURL: "https://som.example/callback", SiteURL: "https://som.example/wallet", BaseURL: s.URL, HTTPClient: s.Client()})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func kind(t *testing.T, e error, want string) {
	t.Helper()
	var se *domain.SendError
	if !errors.As(e, &se) || se.Kind != want {
		t.Fatalf("error %v; want %s", e, want)
	}
	if strings.Contains(e.Error(), "secret") {
		t.Fatal("leaked body")
	}
}
func TestOAuthProtocolAndTenant(t *testing.T) {
	for _, tc := range []struct {
		name, tenant, app string
		want              string
	}{{"ok", "tenant-test", "", ""}, {"other tenant", "other", "", "permission"}, {"empty tenant", "", "", "permission"}, {"other app", "tenant-test", "cli_other", "permission"}} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/v3/token":
					var b map[string]string
					json.NewDecoder(r.Body).Decode(&b)
					if r.Method != "POST" || b["client_id"] != "cli_test" || b["client_secret"] != "dummy-secret" || b["code"] != "test-code" || b["grant_type"] != "authorization_code" || b["redirect_uri"] != "https://som.example/callback" {
						t.Error("wrong token request")
					}
					w.Write([]byte(`{"code":0,"access_token":"test-user-token"}`))
				case "/open-apis/authen/v1/user_info":
					if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-user-token" {
						t.Error("wrong user info auth")
					}
					json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"tenant_key": tc.tenant, "app_id": tc.app, "open_id": "ou_test", "name": "Test"}})
				default:
					t.Error("unexpected path")
					w.WriteHeader(404)
				}
			})
			u, _ := url.Parse(c.AuthorizeURL("a&b"))
			if u.Host != "accounts.feishu.cn" || u.Query().Get("state") != "a&b" || u.Query().Get("client_id") != "cli_test" {
				t.Fatal("invalid authorize URL")
			}
			id, e := c.Exchange(context.Background(), "test-code")
			if tc.want != "" {
				kind(t, e, tc.want)
			} else if e != nil || id.AppID != "cli_test" || id.OpenID != "ou_test" {
				t.Fatalf("bad identity: %+v %v", id, e)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthProductionOrigins(t *testing.T) {
	var requests int
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		body := `{"code":0,"access_token":"test-user-token"}`
		if requests == 1 {
			if r.URL.String() != "https://accounts.feishu.cn/oauth/v3/token" || r.Method != "POST" || r.Header.Get("Authorization") != "" {
				t.Fatal("OAuth credentials sent to wrong endpoint")
			}
		} else {
			if r.URL.String() != "https://open.feishu.cn/open-apis/authen/v1/user_info" || r.Header.Get("Authorization") != "Bearer test-user-token" {
				t.Fatal("user identity sent to wrong endpoint")
			}
			body = `{"code":0,"data":{"tenant_key":"t","open_id":"ou_test","name":"Test"}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	c, err := New(Config{AppID: "test", AppSecret: "dummy", RedirectURL: "https://som.example/cb", SiteURL: "https://som.example", HTTPClient: h})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exchange(context.Background(), "code"); err != nil || requests != 2 {
		t.Fatalf("exchange: %v, requests: %d", err, requests)
	}
}
func TestSendProtocolAndConcurrentTokenCache(t *testing.T) {
	var tokens atomic.Int32
	var sends atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			tokens.Add(1)
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["app_id"] != "cli_test" || b["app_secret"] != "dummy-secret" {
				t.Error("wrong app token body")
			}
			w.Write([]byte(`{"code":0,"tenant_access_token":"test-tenant-token","expire":7200}`))
			return
		}
		if r.URL.Path != "/open-apis/im/v1/messages" || r.Method != "POST" || r.URL.Query().Get("receive_id_type") != "open_id" || r.Header.Get("Authorization") != "Bearer test-tenant-token" {
			t.Error("wrong send request")
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["uuid"] != "stable-event" || b["receive_id"] != "ou_test" || b["msg_type"] != "interactive" {
			t.Error("wrong fields")
		}
		if strings.Contains(b["content"], "attacker") || !strings.Contains(b["content"], "https://som.example/usage") || !strings.Contains(b["content"], "https://som.example/profile") || strings.Contains(b["content"], "lark_md") {
			t.Error("unsafe card")
		}
		var card map[string]any
		if json.Unmarshal([]byte(b["content"]), &card) != nil {
			t.Error("content is not serialized JSON")
		}
		sends.Add(1)
		w.Write([]byte(`{"code":0,"data":{"message_id":"om_test"}}`))
	})
	m := domain.Message{EventID: "stable-event", OpenID: "ou_test", DisplayName: "<at id=attacker>**", SiteURL: "https://attacker/", Balance: 1_000_000, Threshold: 10_000_000}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, e := c.Send(context.Background(), m)
			if e != nil || id != "om_test" {
				t.Errorf("send: %s %v", id, e)
			}
		}()
	}
	wg.Wait()
	if tokens.Load() != 1 || sends.Load() != 12 {
		t.Fatalf("tokens %d sends %d", tokens.Load(), sends.Load())
	}
	c.mu.Lock()
	c.expires = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if _, e := c.Send(context.Background(), m); e != nil {
		t.Fatal(e)
	}
	if tokens.Load() != 2 {
		t.Fatal("expired token reused")
	}
}
func TestSendFailureClassification(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body, want string
	}{{"http-token", 401, `secret`, "token"}, {"server", 503, `secret`, "uncertain"}, {"rate", 429, `secret`, "rate_limit"}, {"permission", 403, `secret`, "permission"}, {"token", 200, `{"code":99991663,"msg":"secret","error":{"message":"secret"}}`, "token"}, {"recipient", 400, `{"code":230013,"msg":"secret"}`, "recipient"}, {"badJSON", 200, `secret`, "uncertain"}, {"missingID", 200, `{"code":0,"data":{}}`, "uncertain"}, {"unknown", 400, `{"code":230001,"msg":"secret"}`, "protocol"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			c.token = "cached"
			c.expires = time.Now().Add(time.Hour)
			_, e := c.Send(context.Background(), domain.Message{EventID: "ev", OpenID: "ou_test"})
			kind(t, e, tc.want)
			if n.Load() != 1 {
				t.Fatal("unexpected retry")
			}
			if tc.want == "token" && c.token != "" {
				t.Fatal("token not invalidated")
			}
			if tc.want == "rate_limit" && e.(*domain.SendError).RetryAfter != 7*time.Second {
				t.Fatal("Retry-After ignored")
			}
		})
	}
}
func TestTimeoutIsUncertain(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	})
	c.token = "cached"
	c.expires = time.Now().Add(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, e := c.Send(ctx, domain.Message{EventID: "ev", OpenID: "ou_test"})
	kind(t, e, "uncertain")
}
func TestOriginAndRedirectGuard(t *testing.T) {
	cfg := Config{AppID: "test", AppSecret: "dummy", TenantKey: "t", SiteURL: "https://som.example", RedirectURL: "https://som.example/cb", BaseURL: "https://attacker.example", HTTPClient: &http.Client{}}
	if _, e := New(cfg); e == nil {
		t.Fatal("external origin accepted")
	}
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	_, e := c.Exchange(context.Background(), "code")
	if e == nil || leaked.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestTokenMarginAndFetchFailure(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"tenant_access_token":"short","expire":10}`))
	})
	before := time.Now()
	if _, e := c.tenantToken(context.Background()); e != nil {
		t.Fatal(e)
	}
	if c.expires.Before(before.Add(8*time.Second)) || c.expires.After(time.Now().Add(9*time.Second)) {
		t.Fatal("missing early expiry margin")
	}
	var calls atomic.Int32
	failed := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/open-apis/auth/v3/tenant_access_token/internal" {
			t.Error("sent despite token failure")
		}
		w.WriteHeader(503)
	})
	_, e := failed.Send(context.Background(), domain.Message{EventID: "ev", OpenID: "ou_test"})
	kind(t, e, "transient")
	if calls.Load() != 1 {
		t.Fatal("unexpected retries")
	}
}

func TestCardMoneyTimeAndLevels(t *testing.T) {
	for _, tc := range []struct {
		level int
		label string
	}{{1, "钱包警戒"}, {2, "钱包紧急"}, {3, "钱包耗尽"}} {
		t.Run(tc.label, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				var request map[string]string
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				var card struct {
					Header struct {
						Title struct {
							Content string `json:"content"`
						} `json:"title"`
					} `json:"header"`
					Elements []struct {
						Text struct {
							Content string `json:"content"`
						} `json:"text"`
						Actions []struct {
							URL string `json:"url"`
						} `json:"actions"`
					} `json:"elements"`
				}
				if err := json.Unmarshal([]byte(request["content"]), &card); err != nil {
					t.Fatal(err)
				}
				if card.Header.Title.Content != "SOM AI HUB · "+tc.label {
					t.Errorf("wrong title %s", card.Header.Title.Content)
				}
				text := card.Elements[0].Text.Content
				for _, want := range []string{tc.label, "0.000001 USD", "1.234567 USD", "2026-10-11 01:02:03（Asia/Shanghai，UTC+8）"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %s in %s", want, text)
					}
				}
				if len(card.Elements[1].Actions) != 2 || card.Elements[1].Actions[0].URL != "https://som.example/usage" || card.Elements[1].Actions[1].URL != "https://som.example/profile" {
					t.Error("unexpected card buttons")
				}
				w.Write([]byte(`{"code":0,"data":{"message_id":"om_card"}}`))
			})
			c.token = "cached"
			c.expires = time.Now().Add(time.Hour)
			_, err := c.Send(context.Background(), domain.Message{EventID: "ev", OpenID: "ou_test", Level: tc.level, Balance: 1, Threshold: 1_234_567, At: time.Date(2026, 10, 10, 17, 2, 3, 0, time.UTC)})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestFormatMicrosExact(t *testing.T) {
	for _, tc := range []struct {
		value int64
		want  string
	}{{0, "0"}, {1, "0.000001"}, {-1, "-0.000001"}, {1_230_000, "1.23"}, {10_000_000, "10"}, {9_223_372_036_854_775_807, "9223372036854.775807"}, {-9_223_372_036_854_775_808, "-9223372036854.775808"}} {
		if got := formatMicros(tc.value); got != tc.want {
			t.Errorf("formatMicros(%d) = %s; want %s", tc.value, got, tc.want)
		}
	}
}

func TestOAuthWithoutEnterpriseAllowlist(t *testing.T) {
	for _, tenant := range []string{"tenant-a", "tenant-b", ""} {
		t.Run(tenant, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/v3/token":
					w.Write([]byte(`{"code":0,"access_token":"test-user-token"}`))
				case "/open-apis/authen/v1/user_info":
					json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"tenant_key": tenant, "open_id": "ou_same", "name": "Test"}})
				default:
					t.Error("unexpected enterprise-info request")
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			c, e := New(Config{AppID: "cli_test", AppSecret: "dummy", RedirectURL: "https://som.example/callback", SiteURL: "https://som.example", BaseURL: srv.URL, HTTPClient: srv.Client()})
			if e != nil {
				t.Fatal(e)
			}
			id, e := c.Exchange(context.Background(), "test-code")
			if tenant == "" {
				kind(t, e, "permission")
			} else if e != nil || id.TenantKey != tenant || id.AppID != "cli_test" {
				t.Fatal("namespace not preserved", e)
			}
		})
	}
}
