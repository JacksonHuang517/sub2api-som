// Package subscriptions monitors limited subscription windows independently of wallets.
package subscriptions

import (
	"errors"
	"som/feishu/internal/domain"
	"time"
)

func Validate(s domain.SubscriptionSnapshot, id int64, now time.Time) error {
	if s.UserID != id || s.Currency != "USD" || s.At.IsZero() || s.At.After(now.Add(30*time.Second)) || now.Sub(s.At) > 5*time.Minute || s.Subscriptions == nil {
		return errors.New("SUBSCRIPTION_INVALID_OR_STALE")
	}
	seen := map[int64]bool{}
	for _, sub := range s.Subscriptions {
		if sub.SubscriptionID <= 0 || sub.GroupID <= 0 || sub.ExpiresAt.IsZero() || seen[sub.SubscriptionID] || len(sub.Windows) != 3 {
			return errors.New("SUBSCRIPTION_INVALID")
		}
		seen[sub.SubscriptionID] = true
		kinds := map[string]bool{}
		for _, q := range sub.Windows {
			if (q.Kind != "daily" && q.Kind != "weekly" && q.Kind != "monthly") || kinds[q.Kind] || q.UsedMicros < 0 || q.UsedMicros > 1e15 {
				return errors.New("SUBSCRIPTION_INVALID")
			}
			kinds[q.Kind] = true
			if q.Limited {
				if q.LimitMicros == nil || *q.LimitMicros <= 0 || *q.LimitMicros > 1e15 || q.RemainingMicros == nil {
					return errors.New("SUBSCRIPTION_INVALID")
				}
				remaining := *q.LimitMicros - q.UsedMicros
				if remaining < 0 {
					remaining = 0
				}
				if *q.RemainingMicros != remaining {
					return errors.New("SUBSCRIPTION_INVALID")
				}
			} else if q.LimitMicros != nil || q.RemainingMicros != nil || q.ResetAt != nil {
				return errors.New("SUBSCRIPTION_INVALID")
			}
			if q.WindowStart != nil && (q.WindowStart.IsZero() || q.WindowStart.After(s.At)) {
				return errors.New("SUBSCRIPTION_INVALID")
			}
			if q.ResetAt != nil && (q.WindowStart == nil || !q.ResetAt.After(s.At) || !q.ResetAt.After(*q.WindowStart) || !q.ResetAt.Before(sub.ExpiresAt)) {
				return errors.New("SUBSCRIPTION_INVALID")
			}
		}
	}
	return nil
}
func eligible(s domain.SubscriptionSnapshot, sub domain.SubscriptionQuota, q domain.QuotaWindow, now time.Time) bool {
	return s.Active && sub.Status == "active" && sub.ExpiresAt.After(now) && q.Limited && q.WindowStart != nil && (q.ResetAt == nil || q.ResetAt.After(now))
}
func severity(q domain.QuotaWindow, warning, critical int64) int {
	if q.UsedMicros >= *q.LimitMicros {
		return 3
	}
	if q.UsedMicros*100 >= *q.LimitMicros*critical {
		return 2
	}
	if q.UsedMicros*100 >= *q.LimitMicros*warning {
		return 1
	}
	return 0
}
func start(q domain.QuotaWindow) string { return q.WindowStart.UTC().Format(time.RFC3339Nano) }
