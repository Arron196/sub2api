package repository

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTelegramStateTestRepository(t *testing.T) (*telegramStateRepository, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &telegramStateRepository{
		rdb:    rdb,
		random: cryptorand.Reader,
		now:    func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) },
	}, mr
}

func TestTelegramStateRepositoryIssueStatusAndSingleUse(t *testing.T) {
	repo, mr := newTelegramStateTestRepository(t)
	ctx := context.Background()

	issued, err := repo.IssueVerificationCode(ctx, 42)
	require.NoError(t, err)
	require.Len(t, issued.Code, service.TelegramVerificationCodeLength)
	for _, ch := range issued.Code {
		require.True(t, ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z')
	}
	require.Equal(t, service.TelegramVerificationCodeTTL, issued.ExpiresAt.Sub(repo.now()))

	status, err := repo.GetVerificationCodeStatus(ctx, 42)
	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, issued.ExpiresAt, status.ExpiresAt)

	codeHash := hashTelegramVerificationCode(issued.Code)
	adminKey := telegramVerificationAdminKeyPrefix + "42"
	codeKey := telegramVerificationCodeKeyPrefix + codeHash
	require.ElementsMatch(t, []string{adminKey, codeKey}, mr.Keys())
	storedHash, err := mr.Get(adminKey)
	require.NoError(t, err)
	require.Equal(t, codeHash, storedHash)
	storedAdminID, err := mr.Get(codeKey)
	require.NoError(t, err)
	require.Equal(t, "42", storedAdminID)
	require.Equal(t, service.TelegramVerificationCodeTTL, mr.TTL(adminKey))
	require.Equal(t, service.TelegramVerificationCodeTTL, mr.TTL(codeKey))

	adminUserID, remainingTTL, err := repo.ConsumeVerificationCode(ctx, issued.Code)
	require.NoError(t, err)
	require.Equal(t, int64(42), adminUserID)
	require.Equal(t, service.TelegramVerificationCodeTTL, remainingTTL)

	_, _, err = repo.ConsumeVerificationCode(ctx, issued.Code)
	require.ErrorIs(t, err, service.ErrTelegramVerificationCodeInvalid)
	status, err = repo.GetVerificationCodeStatus(ctx, 42)
	require.NoError(t, err)
	require.Nil(t, status)
}

func TestTelegramStateRepositoryNewIssueInvalidatesPreviousCode(t *testing.T) {
	repo, _ := newTelegramStateTestRepository(t)
	repo.random = bytes.NewReader(append(
		bytes.Repeat([]byte{0}, service.TelegramVerificationCodeLength),
		bytes.Repeat([]byte{1}, service.TelegramVerificationCodeLength)...,
	))
	ctx := context.Background()

	first, err := repo.IssueVerificationCode(ctx, 42)
	require.NoError(t, err)
	second, err := repo.IssueVerificationCode(ctx, 42)
	require.NoError(t, err)
	require.NotEqual(t, first.Code, second.Code)

	_, _, err = repo.ConsumeVerificationCode(ctx, first.Code)
	require.ErrorIs(t, err, service.ErrTelegramVerificationCodeInvalid)
	adminUserID, _, err := repo.ConsumeVerificationCode(ctx, second.Code)
	require.NoError(t, err)
	require.Equal(t, int64(42), adminUserID)
}

func TestTelegramStateRepositoryExpiryAndCancel(t *testing.T) {
	repo, mr := newTelegramStateTestRepository(t)
	ctx := context.Background()

	issued, err := repo.IssueVerificationCode(ctx, 9)
	require.NoError(t, err)
	mr.FastForward(service.TelegramVerificationCodeTTL)
	status, err := repo.GetVerificationCodeStatus(ctx, 9)
	require.NoError(t, err)
	require.Nil(t, status)
	_, _, err = repo.ConsumeVerificationCode(ctx, issued.Code)
	require.ErrorIs(t, err, service.ErrTelegramVerificationCodeInvalid)

	issued, err = repo.IssueVerificationCode(ctx, 9)
	require.NoError(t, err)
	cancelled, err := repo.CancelVerificationCode(ctx, 9)
	require.NoError(t, err)
	require.True(t, cancelled)
	cancelled, err = repo.CancelVerificationCode(ctx, 9)
	require.NoError(t, err)
	require.False(t, cancelled)
	_, _, err = repo.ConsumeVerificationCode(ctx, issued.Code)
	require.ErrorIs(t, err, service.ErrTelegramVerificationCodeInvalid)
}

func TestTelegramStateRepositoryVerificationAttemptLimitAndExpiry(t *testing.T) {
	repo, mr := newTelegramStateTestRepository(t)
	ctx := context.Background()
	key := telegramVerificationAttemptKeyPrefix + "88001"

	for attempt := 1; attempt <= service.TelegramVerificationAttemptLimit; attempt++ {
		allowed, err := repo.AllowVerificationAttempt(ctx, 88001)
		require.NoError(t, err)
		require.True(t, allowed)
	}
	require.Equal(t, service.TelegramVerificationAttemptTTL, mr.TTL(key))
	allowed, err := repo.AllowVerificationAttempt(ctx, 88001)
	require.NoError(t, err)
	require.False(t, allowed)

	mr.FastForward(service.TelegramVerificationAttemptTTL)
	allowed, err = repo.AllowVerificationAttempt(ctx, 88001)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Equal(t, service.TelegramVerificationAttemptTTL, mr.TTL(key))
	require.NoError(t, repo.ClearVerificationAttempts(ctx, 88001))
	require.False(t, mr.Exists(key))
}

func TestTelegramStateRepositoryUpdateDedupAndPendingInput(t *testing.T) {
	repo, mr := newTelegramStateTestRepository(t)
	ctx := context.Background()

	claimed, err := repo.ClaimUpdate(ctx, 1001)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = repo.ClaimUpdate(ctx, 1001)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, repo.ReleaseUpdate(ctx, 1001))
	claimed, err = repo.ClaimUpdate(ctx, 1001)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repo.CompleteUpdate(ctx, 1001))
	claimed, err = repo.ClaimUpdate(ctx, 1001)
	require.NoError(t, err)
	require.False(t, claimed)
	mr.FastForward(service.TelegramUpdateDeduplicationTTL)
	claimed, err = repo.ClaimUpdate(ctx, 1001)
	require.NoError(t, err)
	require.True(t, claimed)

	pending := service.TelegramPendingSettingInput{
		Flow:            "group_rate",
		SettingKey:      " site.name ",
		InputType:       " text ",
		GroupID:         42,
		RateKind:        service.TelegramGroupRateKindBase,
		ReturnPage:      3,
		OriginChatID:    88001,
		OriginMessageID: 91,
		OperationNonce:  "0123456789abcdef",
		BindingID:       7,
		AdminUserID:     42,
		GroupUpdatedAt:  repo.now().Add(-time.Minute),
		ExpiresAt:       repo.now().Add(2 * time.Minute),
	}
	require.NoError(t, repo.SetPendingSettingInput(ctx, 88001, pending))
	stored, err := repo.GetPendingSettingInput(ctx, 88001)
	require.NoError(t, err)
	require.Equal(t, "site.name", stored.SettingKey)
	require.Equal(t, service.TelegramSettingInputType("text"), stored.InputType)
	require.Equal(t, pending.Flow, stored.Flow)
	require.Equal(t, pending.GroupID, stored.GroupID)
	require.Equal(t, pending.RateKind, stored.RateKind)
	require.Equal(t, pending.ReturnPage, stored.ReturnPage)
	require.Equal(t, pending.OriginChatID, stored.OriginChatID)
	require.Equal(t, pending.OriginMessageID, stored.OriginMessageID)
	require.Equal(t, pending.OperationNonce, stored.OperationNonce)
	require.Equal(t, pending.BindingID, stored.BindingID)
	require.Equal(t, pending.AdminUserID, stored.AdminUserID)
	require.True(t, pending.GroupUpdatedAt.Equal(stored.GroupUpdatedAt))

	taken, err := repo.TakePendingSettingInputIfNonce(ctx, 88001, "fedcba9876543210")
	require.NoError(t, err)
	require.Nil(t, taken)
	stored, err = repo.GetPendingSettingInput(ctx, 88001)
	require.NoError(t, err)
	require.NotNil(t, stored)

	taken, err = repo.TakePendingSettingInputIfNonce(ctx, 88001, pending.OperationNonce)
	require.NoError(t, err)
	require.Equal(t, stored, taken)
	stored, err = repo.GetPendingSettingInput(ctx, 88001)
	require.NoError(t, err)
	require.Nil(t, stored)
	deleted, err := repo.DeletePendingSettingInput(ctx, 88001)
	require.NoError(t, err)
	require.False(t, deleted)
}

func TestTelegramStateRepositoryTakePendingInputIfNonceIsAtomic(t *testing.T) {
	repo, _ := newTelegramStateTestRepository(t)
	ctx := context.Background()
	pending := service.TelegramPendingSettingInput{
		Flow:           "group_rate",
		SettingKey:     "__group_rate__",
		InputType:      service.TelegramSettingTypeFloat,
		OperationNonce: "0123456789abcdef",
		ExpiresAt:      repo.now().Add(time.Minute),
	}
	require.NoError(t, repo.SetPendingSettingInput(ctx, 88001, pending))

	var successful atomic.Int32
	var failed atomic.Int32
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			taken, err := repo.TakePendingSettingInputIfNonce(ctx, 88001, pending.OperationNonce)
			if err != nil {
				failed.Add(1)
				return
			}
			if taken != nil {
				successful.Add(1)
			}
		}()
	}
	wait.Wait()
	require.Zero(t, failed.Load())
	require.Equal(t, int32(1), successful.Load())
}

func TestTelegramStateRepositoryRestorePreservesRemainingTTL(t *testing.T) {
	repo, mr := newTelegramStateTestRepository(t)
	ctx := context.Background()
	issued, err := repo.IssueVerificationCode(ctx, 42)
	require.NoError(t, err)
	mr.FastForward(2 * time.Minute)

	adminUserID, remainingTTL, err := repo.ConsumeVerificationCode(ctx, issued.Code)
	require.NoError(t, err)
	require.Equal(t, int64(42), adminUserID)
	require.Equal(t, 3*time.Minute, remainingTTL)
	restored, err := repo.RestoreVerificationCode(ctx, issued.Code, adminUserID, remainingTTL)
	require.NoError(t, err)
	require.True(t, restored)
	require.Equal(t, remainingTTL, mr.TTL(telegramVerificationCodeKeyPrefix+hashTelegramVerificationCode(issued.Code)))

	restored, err = repo.RestoreVerificationCode(ctx, issued.Code, adminUserID, remainingTTL)
	require.NoError(t, err)
	require.False(t, restored)
	adminUserID, _, err = repo.ConsumeVerificationCode(ctx, issued.Code)
	require.NoError(t, err)
	require.Equal(t, int64(42), adminUserID)
}

func TestTelegramStateRepositoryConfigLockOwnership(t *testing.T) {
	repo, _ := newTelegramStateTestRepository(t)
	ctx := context.Background()
	acquired, err := repo.AcquireConfigLock(ctx, "owner-a", service.TelegramConfigLockTTL)
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = repo.AcquireConfigLock(ctx, "owner-b", service.TelegramConfigLockTTL)
	require.NoError(t, err)
	require.False(t, acquired)
	require.NoError(t, repo.ReleaseConfigLock(ctx, "owner-b"))
	acquired, err = repo.AcquireConfigLock(ctx, "owner-b", service.TelegramConfigLockTTL)
	require.NoError(t, err)
	require.False(t, acquired)
	require.NoError(t, repo.ReleaseConfigLock(ctx, "owner-a"))
	acquired, err = repo.AcquireConfigLock(ctx, "owner-b", service.TelegramConfigLockTTL)
	require.NoError(t, err)
	require.True(t, acquired)
}
