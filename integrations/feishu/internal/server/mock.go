package server

import (
	"context"
	"errors"
	"net/url"
	"som/feishu/internal/domain"
	"sync"
	"time"
)

// Mock never contacts a network and is only wired by the explicit loopback mode.
type Mock struct {
	Origin  string
	mu      sync.Mutex
	balance int64
	last    time.Time
}

func (m *Mock) AuthorizeURL(state string) string {
	return m.Origin + prefix + "/oauth/callback?code=local-fixture&state=" + url.QueryEscape(state)
}
func (m *Mock) Exchange(ctx context.Context, code string) (domain.Identity, error) {
	if code != "local-fixture" {
		return domain.Identity{}, errors.New("invalid mock code")
	}
	return domain.Identity{TenantKey: "mock-tenant", AppID: "mock-app", OpenID: "mock-open-id", DisplayName: "张三 · 演示"}, nil
}
func (m *Mock) Send(context.Context, domain.Message) (string, error) {
	return "", errors.New("mock must never send")
}
func (m *Mock) Set(balance int64) { m.mu.Lock(); defer m.mu.Unlock(); m.balance = balance }
func (m *Mock) Fetch(ctx context.Context, id int64) (domain.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return domain.Snapshot{UserID: id, Balance: m.balance, Currency: "USD", Active: true, At: time.Now().UTC()}, nil
}

func (m *Mock) FetchSubscriptions(ctx context.Context, id int64) (domain.SubscriptionSnapshot, error) {
	now := time.Now().UTC()
	start := now.Truncate(24 * time.Hour)
	reset := start.Add(24 * time.Hour)
	limit := int64(100000000)
	used := int64(81000000)
	remaining := limit - used
	return domain.SubscriptionSnapshot{UserID: id, Active: true, Currency: "USD", At: now, Subscriptions: []domain.SubscriptionQuota{{SubscriptionID: 1, GroupID: 1, GroupName: "本地演示订阅", Status: "active", ExpiresAt: now.Add(60 * 24 * time.Hour), Windows: []domain.QuotaWindow{{Kind: "daily", Limited: true, LimitMicros: &limit, UsedMicros: used, RemainingMicros: &remaining, WindowStart: &start, ResetAt: &reset}, {Kind: "weekly"}, {Kind: "monthly"}}}}}, nil
}
