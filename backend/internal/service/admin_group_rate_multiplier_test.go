package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminServiceUpdateGroupRateMultiplierIsNarrowAndInvalidatesCache(t *testing.T) {
	daily, weekly, monthly := 10.0, 20.0, 30.0
	repo := &telegramGroupRateRepoStub{group: &Group{
		ID: 42, Name: "Primary", Platform: PlatformOpenAI, Status: StatusActive,
		RateMultiplier: 1.0, DailyLimitUSD: &daily, WeeklyLimitUSD: &weekly, MonthlyLimitUSD: &monthly,
		ImageRateIndependent: true, ImageRateMultiplier: 1.25,
	}}
	invalidator := &telegramGroupRateInvalidatorStub{}
	service := &adminServiceImpl{groupRepo: repo, authCacheInvalidator: invalidator}

	updated, err := service.UpdateGroupRateMultiplier(context.Background(), 42, TelegramGroupRateKindBase, 1.75)
	require.NoError(t, err)
	require.Equal(t, 1.75, updated.RateMultiplier)
	require.Equal(t, 1.25, updated.ImageRateMultiplier)
	require.Equal(t, &daily, updated.DailyLimitUSD)
	require.Equal(t, &weekly, updated.WeeklyLimitUSD)
	require.Equal(t, &monthly, updated.MonthlyLimitUSD)
	require.Equal(t, 1, repo.updateCalls)
	require.Zero(t, repo.fullUpdateCalls)
	require.Equal(t, []int64{42}, invalidator.groupIDs)

	_, err = service.UpdateGroupRateMultiplier(context.Background(), 42, TelegramGroupRateKindBase, 0)
	require.Error(t, err)
	require.Equal(t, 1, repo.updateCalls)
}

func TestAdminServiceUpdateGroupRateMultiplierRequiresApplicableMode(t *testing.T) {
	repo := &telegramGroupRateRepoStub{group: &Group{
		ID: 42, Name: "Primary", Platform: PlatformOpenAI, Status: StatusActive,
		RateMultiplier: 1, ImageRateIndependent: false, ImageRateMultiplier: 1,
	}}
	service := &adminServiceImpl{groupRepo: repo}

	_, err := service.UpdateGroupRateMultiplier(context.Background(), 42, TelegramGroupRateKindImage, 2)
	require.ErrorContains(t, err, "not editable")
	require.Zero(t, repo.updateCalls)
}

type telegramGroupRateRepoStub struct {
	AdminGroupRepository
	group           *Group
	updateCalls     int
	fullUpdateCalls int
}

func (r *telegramGroupRateRepoStub) GetByID(context.Context, int64) (*Group, error) {
	copy := *r.group
	return &copy, nil
}

func (r *telegramGroupRateRepoStub) Update(_ context.Context, group *Group) error {
	copy := *group
	r.group = &copy
	r.fullUpdateCalls++
	return nil
}

func (r *telegramGroupRateRepoStub) UpdateRateMultiplier(_ context.Context, _ int64, kind string, multiplier float64) (*Group, error) {
	copy := *r.group
	switch kind {
	case TelegramGroupRateKindBase:
		copy.RateMultiplier = multiplier
	case TelegramGroupRateKindImage:
		copy.ImageRateMultiplier = multiplier
	case TelegramGroupRateKindVideo:
		copy.VideoRateMultiplier = multiplier
	case TelegramGroupRateKindPeak:
		copy.PeakRateMultiplier = multiplier
	}
	r.group = &copy
	r.updateCalls++
	return &copy, nil
}

type telegramGroupRateInvalidatorStub struct {
	groupIDs []int64
}

func (*telegramGroupRateInvalidatorStub) InvalidateAuthCacheByKey(context.Context, string)   {}
func (*telegramGroupRateInvalidatorStub) InvalidateAuthCacheByUserID(context.Context, int64) {}
func (s *telegramGroupRateInvalidatorStub) InvalidateAuthCacheByGroupID(_ context.Context, groupID int64) {
	s.groupIDs = append(s.groupIDs, groupID)
}
