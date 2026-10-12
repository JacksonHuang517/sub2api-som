// Package feishu implements the enterprise-internal Feishu OAuth and IM protocols.
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"som/feishu/internal/domain"
)

const productionBase = "https://open.feishu.cn"
const productionOAuthBase = "https://accounts.feishu.cn"

type Config struct {
	AppID, AppSecret, TenantKey, RedirectURL, SiteURL string
	HTTPClient                                        *http.Client
	// BaseURL is restricted to the production origin or a loopback httptest server.
	BaseURL string
}
type Client struct {
	cfg     Config
	http    *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
}

var _ domain.Feishu = (*Client)(nil)

func New(cfg Config) (*Client, error) {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, errors.New("feishu configuration incomplete")
	}
	for _, raw := range []string{cfg.RedirectURL, cfg.SiteURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
			return nil, errors.New("feishu invalid site or redirect URL")
		}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = productionBase
	}
	u, e := url.Parse(cfg.BaseURL)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (cfg.BaseURL != productionBase && !(cfg.HTTPClient != nil && u.Scheme == "http" && loopback(u.Hostname()))) {
		return nil, errors.New("feishu invalid API origin")
	}
	h := http.Client{Timeout: 15 * time.Second}
	if cfg.HTTPClient != nil {
		h = *cfg.HTTPClient
		if h.Timeout <= 0 {
			h.Timeout = 15 * time.Second
		}
	}
	// Never forward credentials to an API redirect target.
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, http: &h}, nil
}
func loopback(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }
func (c *Client) AuthorizeURL(state string) string {
	q := url.Values{"client_id": {c.cfg.AppID}, "response_type": {"code"}, "redirect_uri": {c.cfg.RedirectURL}, "state": {state}}
	return "https://accounts.feishu.cn/open-apis/authen/v1/authorize?" + q.Encode()
}
func (c *Client) Exchange(ctx context.Context, code string) (domain.Identity, error) {
	var none domain.Identity
	if code == "" {
		return none, &domain.SendError{Kind: "protocol"}
	}
	var tok struct {
		Code        *int            `json:"code"`
		AccessToken string          `json:"access_token"`
		Error       json.RawMessage `json:"error"`
	}
	// OAuth v3 uses the accounts origin; the validated loopback override is test-only.
	oauthBase := productionOAuthBase
	if c.cfg.BaseURL != productionBase {
		oauthBase = c.cfg.BaseURL
	}
	err := c.callOrigin(ctx, oauthBase, http.MethodPost, "/oauth/v3/token", "", map[string]string{"grant_type": "authorization_code", "client_id": c.cfg.AppID, "client_secret": c.cfg.AppSecret, "code": code, "redirect_uri": c.cfg.RedirectURL}, &tok, false)
	if err != nil {
		return none, err
	}
	if tok.AccessToken == "" {
		return none, &domain.SendError{Kind: "token"}
	}
	var info struct {
		Data struct {
			OpenID    string `json:"open_id"`
			TenantKey string `json:"tenant_key"`
			Name      string `json:"name"`
			AppID     string `json:"app_id"`
		} `json:"data"`
	}
	if err = c.call(ctx, http.MethodGet, "/open-apis/authen/v1/user_info", tok.AccessToken, nil, &info, false); err != nil {
		return none, err
	}
	if info.Data.TenantKey == "" || (c.cfg.TenantKey != "" && info.Data.TenantKey != c.cfg.TenantKey) || (info.Data.AppID != "" && info.Data.AppID != c.cfg.AppID) {
		return none, &domain.SendError{Kind: "permission"}
	}
	if !strings.HasPrefix(info.Data.OpenID, "ou_") {
		return none, &domain.SendError{Kind: "protocol"}
	}
	// user_info has no required app_id claim. The authenticated code exchange binds
	// this OpenID to our configured client; always persist that namespace explicitly.
	return domain.Identity{TenantKey: info.Data.TenantKey, AppID: c.cfg.AppID, OpenID: info.Data.OpenID, DisplayName: info.Data.Name}, nil
}
func (c *Client) tenantToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	var r struct {
		Token  string `json:"tenant_access_token"`
		Expire int64  `json:"expire"`
	}
	start := time.Now()
	if err := c.call(ctx, http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", "", map[string]string{"app_id": c.cfg.AppID, "app_secret": c.cfg.AppSecret}, &r, false); err != nil {
		return "", err
	}
	if r.Token == "" || r.Expire <= 0 || r.Expire > 86400 {
		return "", &domain.SendError{Kind: "protocol"}
	}
	lifetime := time.Duration(r.Expire) * time.Second
	margin := time.Minute
	if lifetime/10 < margin {
		margin = lifetime / 10
	}
	c.token = r.Token
	c.expires = start.Add(lifetime - margin)
	return c.token, nil
}
func (c *Client) invalidate(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		c.token = ""
		c.expires = time.Time{}
	}
}
func (c *Client) Send(ctx context.Context, m domain.Message) (string, error) {
	if m.EventID == "" || len(m.EventID) > 50 || !strings.HasPrefix(m.OpenID, "ou_") {
		return "", &domain.SendError{Kind: "protocol"}
	}
	token, err := c.tenantToken(ctx)
	if err != nil {
		return "", err
	}
	// Deliberately omit caller-supplied display name: no Markdown, @mention or URL injection.
	level := map[int]string{1: "钱包警戒", 2: "钱包紧急", 3: "钱包耗尽"}[m.Level]
	if level == "" {
		level = "钱包余额提醒"
	}
	observedAt := "未提供"
	if !m.At.IsZero() {
		observedAt = m.At.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05") + "（Asia/Shanghai，UTC+8）"
	}
	text := fmt.Sprintf("%s\n当前钱包余额：%s USD\n提醒阈值：%s USD\n数据时间：%s\n请前往控制台查看钱包与用量详情。", level, formatMicros(m.Balance), formatMicros(m.Threshold), observedAt)
	button := func(label, path string) map[string]any {
		site, _ := url.Parse(c.cfg.SiteURL)
		target := site.ResolveReference(&url.URL{Path: path})
		return map[string]any{"tag": "button", "text": map[string]string{"tag": "plain_text", "content": label}, "type": "default", "url": target.String()}
	}
	card := map[string]any{
		"header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": "SOM AI HUB · " + level}},
		"elements": []any{
			map[string]any{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": text}},
			map[string]any{"tag": "action", "actions": []any{button("查看用量", "/usage"), button("管理提醒", "/profile")}},
		},
	}
	raw, _ := json.Marshal(card)
	msgType := "interactive"
	if m.Subscription != nil {
		q := m.Subscription
		kind := map[string]string{"daily": "每日", "weekly": "每周", "monthly": "每月"}[q.Window.Kind]
		if kind == "" || q.Window.LimitMicros == nil || q.Window.RemainingMicros == nil {
			return "", &domain.SendError{Kind: "protocol"}
		}
		level := map[int]string{1: "订阅用量警戒", 2: "订阅用量紧急", 3: "订阅额度耗尽"}[m.Level]
		site, _ := url.Parse(c.cfg.SiteURL)
		link := func(path string) string { return site.ResolveReference(&url.URL{Path: path}).String() }
		reset := "本窗口到期前不再刷新"
		if q.Window.ResetAt != nil {
			reset = q.Window.ResetAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05 MST")
		}
		// Use text and neutralize Feishu mention syntax in caller-supplied names.
		text := fmt.Sprintf("SOM AI HUB · %s\n订阅：%s（#%d）\n窗口：%s\n已用：%s / %s USD\n剩余：%s USD\n已用比例提醒阈值：%d%%\n窗口重置：%s\n订阅到期：%s\n数据时间：%s\n查看订阅：%s\n管理提醒：%s", level, subscriptionLabel(q.GroupName), q.SubscriptionID, kind, formatMicros(q.Window.UsedMicros), formatMicros(*q.Window.LimitMicros), formatMicros(*q.Window.RemainingMicros), q.Percent, reset, q.ExpiresAt.Format(time.RFC3339), observedAt, link("/subscriptions"), link("/profile"))
		raw, _ = json.Marshal(map[string]string{"text": text})
		msgType = "text"
	}
	var r struct {
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	err = c.call(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=open_id", token, map[string]string{"receive_id": m.OpenID, "msg_type": msgType, "content": string(raw), "uuid": m.EventID}, &r, true)
	if err != nil {
		var se *domain.SendError
		if errors.As(err, &se) && se.Kind == "token" {
			c.invalidate(token)
		}
		return "", err
	}
	if r.Data.MessageID == "" {
		return "", &domain.SendError{Kind: "uncertain"}
	}
	return r.Data.MessageID, nil
}

// formatMicros formats every int64 value exactly, including MinInt64, without floats.
func formatMicros(value int64) string {
	magnitude := uint64(value)
	sign := ""
	if value < 0 {
		sign = "-"
		magnitude = uint64(-(value + 1)) + 1
	}
	whole := strconv.FormatUint(magnitude/1_000_000, 10)
	remainder := magnitude % 1_000_000
	if remainder == 0 {
		return sign + whole
	}
	fraction := strings.TrimRight(fmt.Sprintf("%06d", remainder), "0")
	return sign + whole + "." + fraction
}

func (c *Client) call(ctx context.Context, method, path, token string, body any, out any, sending bool) error {
	return c.callOrigin(ctx, c.cfg.BaseURL, method, path, token, body, out, sending)
}

func (c *Client) callOrigin(ctx context.Context, origin, method, path, token string, body any, out any, sending bool) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return &domain.SendError{Kind: "protocol"}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(data))
	if err != nil {
		return &domain.SendError{Kind: "protocol"}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if sending {
			return &domain.SendError{Kind: "uncertain"}
		}
		return &domain.SendError{Kind: "transient"}
	}
	defer resp.Body.Close()
	retry := retryAfter(resp.Header.Get("Retry-After"))
	if resp.StatusCode == 429 {
		return &domain.SendError{Kind: "rate_limit", RetryAfter: retry}
	}
	if resp.StatusCode >= 500 {
		kind := "transient"
		if sending {
			kind = "uncertain"
		}
		return &domain.SendError{Kind: kind}
	}
	if resp.StatusCode == 401 {
		return &domain.SendError{Kind: "token"}
	}
	if resp.StatusCode == 403 {
		return &domain.SendError{Kind: "permission"}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		kind := "protocol"
		if sending {
			kind = "uncertain"
		}
		return &domain.SendError{Kind: kind}
	}
	var envelope struct {
		Code  *int            `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		kind := "protocol"
		if sending {
			kind = "uncertain"
		}
		return &domain.SendError{Kind: kind}
	}
	if envelope.Code != nil && *envelope.Code != 0 {
		return classify(*envelope.Code, retry)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || len(envelope.Error) != 0 && string(envelope.Error) != "null" || envelope.Code == nil {
		kind := "protocol"
		if sending && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			kind = "uncertain"
		}
		return &domain.SendError{Kind: kind}
	}
	if json.Unmarshal(raw, out) != nil {
		kind := "protocol"
		if sending {
			kind = "uncertain"
		}
		return &domain.SendError{Kind: kind}
	}
	return nil
}
func classify(code int, retry time.Duration) error {
	kind := "protocol"
	switch code {
	case 99991661, 99991663, 99991664, 99991671:
		kind = "token"
	case 99991400, 99991401, 230020:
		kind = "rate_limit"
	case 99991672, 99991679, 99991668, 230006:
		kind = "permission"
	case 230002, 230013:
		kind = "recipient"
	case 99991500:
		kind = "uncertain"
	}
	return &domain.SendError{Kind: kind, RetryAfter: retry}
}
func retryAfter(s string) time.Duration {
	if n, e := strconv.ParseInt(s, 10, 64); e == nil && n >= 0 && n <= 86400 {
		return time.Duration(n) * time.Second
	}
	if t, e := http.ParseTime(s); e == nil {
		d := time.Until(t)
		if d > 0 && d <= 24*time.Hour {
			return d
		}
	}
	return 0
}

// Feishu text supports <at> syntax. Fullwidth brackets prevent names from
// becoming mentions; flatten control characters to keep the message structure.
func subscriptionLabel(name string) string {
	var label []rune
	for _, r := range name {
		if len(label) >= 200 {
			break
		}
		switch {
		case r == '<':
			r = '＜'
		case r == '>':
			r = '＞'
		case r < 32 || r == 127:
			r = ' '
		}
		label = append(label, r)
	}
	return string(label)
}
