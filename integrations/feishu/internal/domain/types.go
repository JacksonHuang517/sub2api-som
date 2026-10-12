package domain

import (
	"context"
	"time"
)

type Identity struct {
	TenantKey   string `json:"tenant_key"`
	AppID       string `json:"app_id"`
	OpenID      string `json:"-"`
	DisplayName string `json:"display_name"`
}
type Binding struct {
	UserID int64 `json:"user_id"`
	Identity
	Version  int64     `json:"version"`
	SyncedAt time.Time `json:"synced_at"`
}
type Preferences struct {
	Enabled  bool  `json:"enabled"`
	Warning  int64 `json:"warning_micros"`
	Critical int64 `json:"critical_micros"`
	Version  int64 `json:"version"`
}
type Snapshot struct {
	UserID   int64     `json:"user_id"`
	Balance  int64     `json:"balance_micros"`
	Currency string    `json:"currency"`
	Active   bool      `json:"active"`
	At       time.Time `json:"at"`
}
type Message struct {
	Subscription *SubscriptionMessage
	EventID      string
	OpenID       string
	DisplayName  string
	Level        int
	Balance      int64
	Threshold    int64
	At           time.Time
	SiteURL      string
}
type SendError struct {
	Kind       string
	RetryAfter time.Duration
}

func (e *SendError) Error() string { return e.Kind }

// Kind: transient, rate_limit, uncertain, permission, recipient, token, protocol.
type Feishu interface {
	AuthorizeURL(state string) string
	Exchange(context.Context, string) (Identity, error)
	Send(context.Context, Message) (string, error)
}
type Balances interface {
	Fetch(context.Context, int64) (Snapshot, error)
}

// Subscription thresholds are integer percentages of used quota, independent of wallet settings.
type SubscriptionPreferences struct {
	Enabled  bool  `json:"enabled"`
	Warning  int64 `json:"warning_percent"`
	Critical int64 `json:"critical_percent"`
	Version  int64 `json:"version"`
}
type SubscriptionSnapshot struct {
	UserID        int64               `json:"user_id"`
	Active        bool                `json:"active"`
	Currency      string              `json:"currency"`
	At            time.Time           `json:"at"`
	Subscriptions []SubscriptionQuota `json:"subscriptions"`
}
type SubscriptionQuota struct {
	SubscriptionID int64         `json:"subscription_id"`
	GroupID        int64         `json:"group_id"`
	GroupName      string        `json:"group_name"`
	Status         string        `json:"status"`
	ExpiresAt      time.Time     `json:"expires_at"`
	Windows        []QuotaWindow `json:"windows"`
}
type QuotaWindow struct {
	Kind            string     `json:"kind"`
	Limited         bool       `json:"limited"`
	LimitMicros     *int64     `json:"limit_micros"`
	UsedMicros      int64      `json:"used_micros"`
	RemainingMicros *int64     `json:"remaining_micros"`
	WindowStart     *time.Time `json:"window_start"`
	ResetAt         *time.Time `json:"reset_at"`
}
type SubscriptionMessage struct {
	SubscriptionID int64
	GroupName      string
	Window         QuotaWindow
	ExpiresAt      time.Time
	Percent        int64
}
type Subscriptions interface {
	FetchSubscriptions(context.Context, int64) (SubscriptionSnapshot, error)
}
