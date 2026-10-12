package service

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/shopspring/decimal"
)

type UserQuotaSnapshot struct {
	UserID        int64                       `json:"user_id"`
	Active        bool                        `json:"active"`
	Currency      string                      `json:"currency"`
	At            time.Time                   `json:"at"`
	Subscriptions []SubscriptionQuotaSnapshot `json:"subscriptions"`
}

type SubscriptionQuotaSnapshot struct {
	SubscriptionID int64                 `json:"subscription_id"`
	GroupID        int64                 `json:"group_id"`
	GroupName      string                `json:"group_name"`
	Status         string                `json:"status"`
	ExpiresAt      time.Time             `json:"expires_at"`
	Windows        []QuotaWindowSnapshot `json:"windows"`
}

type QuotaWindowSnapshot struct {
	Kind            string     `json:"kind"`
	Limited         bool       `json:"limited"`
	LimitMicros     *int64     `json:"limit_micros"`
	UsedMicros      int64      `json:"used_micros"`
	RemainingMicros *int64     `json:"remaining_micros"`
	WindowStart     *time.Time `json:"window_start"`
	ResetAt         *time.Time `json:"reset_at"`
}

// GetUserQuotaSnapshot only reads the repository. Expired windows are projected
// on copies, using the same clock and reset rules as billing, without maintenance.
// Active is filled by the bridge from the user service (not subscription status).
func (s *SubscriptionService) GetUserQuotaSnapshot(ctx context.Context, userID int64) (*UserQuotaSnapshot, error) {
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	subs, err := s.userSubRepo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := &UserQuotaSnapshot{UserID: userID, Currency: "USD", At: now.UTC(), Subscriptions: make([]SubscriptionQuotaSnapshot, 0, len(subs))}
	for _, stored := range subs {
		if stored.DeletedAt != nil {
			continue
		}
		sub := stored
		if sub.Group == nil {
			return nil, errors.New("subscription group unavailable")
		}
		active := sub.Status == SubscriptionStatusActive && sub.ExpiresAt.After(now)
		if sub.Status == SubscriptionStatusActive && !sub.ExpiresAt.After(now) {
			sub.Status = SubscriptionStatusExpired
		}
		if active {
			if start, ok := sub.automaticDailyWindowStartAt(now); ok {
				sub.DailyWindowStart, sub.DailyUsageUSD = &start, 0
			}
			if start, ok := sub.automaticWindowStartAt(sub.WeeklyWindowStart, 7*24*time.Hour, now); ok {
				sub.WeeklyWindowStart, sub.WeeklyUsageUSD = &start, 0
			}
			if start, ok := sub.automaticWindowStartAt(sub.MonthlyWindowStart, 30*24*time.Hour, now); ok {
				sub.MonthlyWindowStart, sub.MonthlyUsageUSD = &start, 0
			}
		}
		item := SubscriptionQuotaSnapshot{SubscriptionID: sub.ID, GroupID: sub.GroupID, GroupName: sub.Group.Name, Status: sub.Status, ExpiresAt: sub.ExpiresAt, Windows: make([]QuotaWindowSnapshot, 0, 3)}
		for _, spec := range []struct {
			kind         string
			limit        *float64
			used         float64
			start, reset *time.Time
		}{
			{"daily", sub.Group.DailyLimitUSD, sub.DailyUsageUSD, sub.DailyWindowStart, sub.DailyResetTime()},
			{"weekly", sub.Group.WeeklyLimitUSD, sub.WeeklyUsageUSD, sub.WeeklyWindowStart, sub.WeeklyResetTime()},
			{"monthly", sub.Group.MonthlyLimitUSD, sub.MonthlyUsageUSD, sub.MonthlyWindowStart, sub.MonthlyResetTime()},
		} {
			used, err := quotaMicros(spec.used)
			if err != nil {
				return nil, err
			}
			window := QuotaWindowSnapshot{Kind: spec.kind, UsedMicros: used, WindowStart: spec.start}
			if spec.limit != nil {
				limit, err := quotaMicros(*spec.limit)
				if err != nil {
					return nil, err
				}
				if *spec.limit > 0 {
					window.Limited, window.LimitMicros = true, &limit
					remaining := limit - used
					if remaining < 0 {
						remaining = 0
					}
					window.RemainingMicros = &remaining
					if active && spec.reset != nil && spec.reset.After(now) && spec.reset.Before(sub.ExpiresAt) {
						window.ResetAt = spec.reset
					}
				}
			}
			item.Windows = append(item.Windows, window)
		}
		result.Subscriptions = append(result.Subscriptions, item)
	}
	return result, nil
}

func quotaMicros(amount float64) (int64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || math.Abs(amount) > 1e9 {
		return 0, errors.New("invalid subscription amount")
	}
	return decimal.NewFromFloat(amount).Shift(6).Round(0).IntPart(), nil
}
