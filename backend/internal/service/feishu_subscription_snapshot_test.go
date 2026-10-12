package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// Unimplemented methods panic, so a maintenance write cannot pass these tests.
type quotaSnapshotReadRepo struct {
	UserSubscriptionRepository
	subs  []UserSubscription
	reads int
}

func (r *quotaSnapshotReadRepo) ListByUserID(context.Context, int64) ([]UserSubscription, error) {
	r.reads++
	return r.subs, nil
}

func TestFeishuQuotaSnapshotProjectsWindowsWithoutWrites(t *testing.T) {
	now := timezone.StartOfDay(time.Now()).Add(12 * time.Hour)
	start := now.Add(-15 * 24 * time.Hour)
	daily := timezone.StartOfDay(start)
	limit := 100.0
	repo := &quotaSnapshotReadRepo{subs: []UserSubscription{{ID: 1, GroupID: 2, Status: SubscriptionStatusActive, StartsAt: start, ExpiresAt: now.Add(40 * 24 * time.Hour), DailyWindowStart: &daily, WeeklyWindowStart: &start, MonthlyWindowStart: &start, DailyUsageUSD: 90, WeeklyUsageUSD: 90, MonthlyUsageUSD: 90, Group: &Group{Name: "quota", DailyLimitUSD: &limit, WeeklyLimitUSD: &limit, MonthlyLimitUSD: &limit}}}}
	svc := &SubscriptionService{userSubRepo: repo, now: func() time.Time { return now }}
	snapshot, err := svc.GetUserQuotaSnapshot(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, 1, repo.reads)
	require.Equal(t, now.UTC(), snapshot.At)
	windows := snapshot.Subscriptions[0].Windows
	require.Zero(t, windows[0].UsedMicros)
	require.True(t, windows[0].WindowStart.Equal(timezone.StartOfDay(now)))
	require.True(t, windows[0].ResetAt.Equal(timezone.StartOfDay(now).AddDate(0, 0, 1)))
	require.Zero(t, windows[1].UsedMicros)
	require.True(t, windows[1].WindowStart.Equal(start.Add(14*24*time.Hour)))
	require.True(t, windows[1].ResetAt.Equal(start.Add(21*24*time.Hour)))
	require.Equal(t, int64(90000000), windows[2].UsedMicros)
	require.True(t, windows[2].ResetAt.Equal(start.Add(30*24*time.Hour)))
	require.Equal(t, 90.0, repo.subs[0].WeeklyUsageUSD)
	require.True(t, repo.subs[0].WeeklyWindowStart.Equal(start))
}

func TestFeishuQuotaSnapshotActivationExpiryAndUnlimited(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	limit, zero := 10.0, 0.0
	for _, tc := range []struct {
		name    string
		sub     UserSubscription
		expired bool
		used    int64
	}{
		{"unactivated", UserSubscription{StartsAt: now, ExpiresAt: now.Add(40 * 24 * time.Hour)}, false, 0},
		{"one time daily", UserSubscription{StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), DailyWindowStart: &now, DailyUsageUSD: 3}, false, 3000000},
		{"exact weekly expiry", UserSubscription{StartsAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour), WeeklyWindowStart: &now}, false, 0},
		{"expired retains usage", UserSubscription{StartsAt: now.Add(-40 * 24 * time.Hour), ExpiresAt: now, DailyWindowStart: &now, DailyUsageUSD: 12}, true, 12000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.sub.Status = SubscriptionStatusActive
			tc.sub.Group = &Group{DailyLimitUSD: &limit, WeeklyLimitUSD: &limit, MonthlyLimitUSD: &zero}
			repo := &quotaSnapshotReadRepo{subs: []UserSubscription{tc.sub}}
			svc := &SubscriptionService{userSubRepo: repo, now: func() time.Time { return now }}
			got, err := svc.GetUserQuotaSnapshot(context.Background(), 42)
			require.NoError(t, err)
			item := got.Subscriptions[0]
			if tc.expired {
				require.Equal(t, SubscriptionStatusExpired, item.Status)
			}
			require.Equal(t, tc.used, item.Windows[0].UsedMicros)
			require.Nil(t, item.Windows[0].ResetAt)
			require.Nil(t, item.Windows[1].ResetAt)
			require.False(t, item.Windows[2].Limited)
			require.Nil(t, item.Windows[2].LimitMicros)
			require.Nil(t, item.Windows[2].RemainingMicros)
			if tc.name == "unactivated" {
				require.Nil(t, item.Windows[0].WindowStart)
				require.Equal(t, int64(10000000), *item.Windows[0].RemainingMicros)
			}
			if tc.expired {
				require.Zero(t, *item.Windows[0].RemainingMicros)
			}
		})
	}
}

func TestFeishuQuotaSnapshotRejectsInvalidAndMissingGroup(t *testing.T) {
	for _, amount := range []float64{math.NaN(), math.Inf(1), 1e10} {
		_, err := quotaMicros(amount)
		require.Error(t, err)
	}
	amount, err := quotaMicros(1.2345675)
	require.NoError(t, err)
	require.Equal(t, int64(1234568), amount)
	svc := &SubscriptionService{userSubRepo: &quotaSnapshotReadRepo{subs: []UserSubscription{{ID: 1}}}}
	_, err = svc.GetUserQuotaSnapshot(context.Background(), 42)
	require.Error(t, err)
}

func TestFeishuQuotaSnapshotLegacyAnchorAndFinalPartialPeriod(t *testing.T) {
	start := time.Date(2026, 7, 1, 13, 37, 6, 0, time.UTC)
	legacy := startOfDay(start)
	limit := 10.0
	for _, elapsed := range []time.Duration{6 * 24 * time.Hour, 8 * 24 * time.Hour} {
		now := start.Add(elapsed)
		repo := &quotaSnapshotReadRepo{subs: []UserSubscription{
			{ID: 1, Status: SubscriptionStatusActive, StartsAt: start, ExpiresAt: start.Add(10 * 24 * time.Hour), WeeklyWindowStart: &legacy, WeeklyUsageUSD: 9, Group: &Group{WeeklyLimitUSD: &limit}},
			{ID: 2, Status: SubscriptionStatusActive, StartsAt: start, ExpiresAt: start.Add(10 * 24 * time.Hour), Group: &Group{}},
		}}
		svc := &SubscriptionService{userSubRepo: repo, now: func() time.Time { return now }}
		got, err := svc.GetUserQuotaSnapshot(context.Background(), 42)
		require.NoError(t, err)
		require.Len(t, got.Subscriptions, 2)
		weekly := got.Subscriptions[0].Windows[1]
		if elapsed < 7*24*time.Hour {
			require.True(t, weekly.ResetAt.Equal(start.Add(7*24*time.Hour)))
			require.Equal(t, int64(9000000), weekly.UsedMicros)
		} else {
			require.True(t, weekly.WindowStart.Equal(start.Add(7*24*time.Hour)))
			require.Nil(t, weekly.ResetAt)
			require.Zero(t, weekly.UsedMicros)
		}
		for _, window := range got.Subscriptions[1].Windows {
			require.False(t, window.Limited)
			require.Nil(t, window.RemainingMicros)
		}
	}
}
