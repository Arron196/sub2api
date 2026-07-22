package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const (
	testTelegramCodeA = "A1B2C3D4E5F6G7H"
	testTelegramCodeB = "J8K9L0M1N2P3Q4R"
	testTelegramCodeC = "S5T6U7V8W9X0Y1Z"
	testTelegramCodeD = "23456789ABCDEFG"
)

func TestTelegramCatalogIsExplicitAndExcludesSecrets(t *testing.T) {
	repo := &telegramSettingRepoStub{values: map[string]string{
		SettingKeySiteName: "Example", SettingKeyRegistrationEnabled: "true",
		SettingKeyAdminAPIKey: "do-not-send", "web_search_emulation_config": `{"providers":{"x":{"api_key":"do-not-send"}}}`,
		"backup_s3_config": `{"secret_access_key":"do-not-send"}`, "unknown_internal_row": "do-not-send",
		SettingKeyTelegramBotTokenEncrypted: "must-not-be-listed",
	}}
	settings := NewSettingService(repo, &config.Config{})
	entries, err := settings.ListTelegramSettingCatalog(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, len(telegramSettingDefinitions))

	for _, excluded := range []string{
		SettingKeyAdminAPIKey, "web_search_emulation_config", "backup_s3_config",
		"unknown_internal_row", SettingKeyTelegramBotTokenEncrypted,
	} {
		for _, entry := range entries {
			require.NotEqual(t, excluded, entry.Key)
			require.NotContains(t, entry.DisplayValue, "do-not-send")
		}
		_, err := settings.ResolveTelegramSetting(context.Background(), telegramSettingID(excluded))
		require.Error(t, err)
	}

	siteName := catalogEntryByKey(t, entries, SettingKeySiteName)
	require.Equal(t, "Example", siteName.DisplayValue)
	require.LessOrEqual(t, len("v:"+siteName.ID), 64)
	resolved, err := settings.ResolveTelegramSetting(context.Background(), siteName.ID)
	require.NoError(t, err)
	require.Equal(t, siteName.Key, resolved.Key)
	require.Equal(t, TelegramSettingTypeBool, catalogEntryByKey(t, entries, SettingKeyRegistrationEnabled).Type)
	require.Equal(t, TelegramSettingTypeInt, catalogEntryByKey(t, entries, SettingKeyDefaultConcurrency).Type)
	require.Equal(t, TelegramSettingTypeFloat, catalogEntryByKey(t, entries, SettingKeyAffiliateRebateRate).Type)
}

func TestTelegramTextCodeBindingIsOneTimeAndRequiresActiveAdmin(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	state.codes[testTelegramCodeA] = 7
	state.attempts[99] = 4
	bindings := newTelegramBindingStub()
	bot := &telegramBotStub{}
	users := telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}}
	service := newTelegramTestService(bindings, state, bot, nil, users)

	update := telegramTextUpdate(1, 99, " a1b2c3-d4e5 f6g7h ")
	require.NoError(t, service.ProcessUpdate(ctx, update))
	require.Contains(t, bot.lastText(), "绑定成功")
	require.NotNil(t, bindings.byTelegram[99])
	require.Empty(t, state.codes)

	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(2, 100, testTelegramCodeA)))
	require.Contains(t, bot.lastText(), "验证码无效或已过期")
	require.Nil(t, bindings.byTelegram[100])

	state.codes[testTelegramCodeB] = 8
	users.users[8] = &User{ID: 8, Role: RoleAdmin, Status: "disabled"}
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(3, 101, testTelegramCodeB)))
	require.Contains(t, bot.lastText(), "验证码无效或已过期")
	require.Nil(t, bindings.byTelegram[101])
}

func TestTelegramVerificationAttemptLimitDoesNotConsumeEleventhCode(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bot := &telegramBotStub{}
	users := telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}}
	service := newTelegramTestService(bindings, state, bot, nil, users)

	for updateID := int64(1); updateID <= TelegramVerificationAttemptLimit; updateID++ {
		require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(updateID, 99, testTelegramCodeC)))
		require.Contains(t, bot.lastText(), "验证码无效或已过期")
	}
	state.codes[testTelegramCodeD] = 7
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(11, 99, testTelegramCodeD)))
	require.Contains(t, bot.lastText(), "尝试次数过多")
	require.Equal(t, int64(7), state.codes[testTelegramCodeD])
	require.Nil(t, bindings.byTelegram[99])
}

func TestTelegramFailedUpdateReleasesDeduplicationLease(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bot := &telegramBotStub{sendErr: errors.New("send failed")}
	service := newTelegramTestService(
		newTelegramBindingStub(),
		state,
		bot,
		nil,
		telegramUserReaderStub{},
	)
	update := telegramTextUpdate(77, 99, "hello")

	require.ErrorContains(t, service.ProcessUpdate(ctx, update), "send failed")
	require.False(t, state.claimed[77])
	bot.sendErr = nil
	require.NoError(t, service.ProcessUpdate(ctx, update))
	require.True(t, state.claimed[77])
	require.Contains(t, bot.lastText(), "生成 15 位字母数字验证码")
}

func TestTelegramBindingLookupFailureDoesNotConsumeVerificationCode(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	state.codes[testTelegramCodeA] = 7
	bindings := newTelegramBindingStub()
	bindings.lookupErr = errors.New("binding store unavailable")
	bot := &telegramBotStub{}
	service := newTelegramTestService(bindings, state, bot, nil, telegramUserReaderStub{})

	err := service.ProcessUpdate(ctx, telegramTextUpdate(1, 99, testTelegramCodeA))
	require.ErrorContains(t, err, "binding store unavailable")
	require.Zero(t, state.attempts[99])
	require.Equal(t, int64(7), state.codes[testTelegramCodeA])
	require.Empty(t, bot.texts)
}

func TestTelegramBindingFailureRestoresVerificationCode(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	state.codes[testTelegramCodeA] = 7
	bindings := newTelegramBindingStub()
	bindings.bindErr = errors.New("binding store unavailable")
	bot := &telegramBotStub{}
	users := telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}}
	service := newTelegramTestService(bindings, state, bot, nil, users)

	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(1, 99, testTelegramCodeA)))
	require.Contains(t, bot.lastText(), "验证码无效或已过期")
	require.Equal(t, int64(7), state.codes[testTelegramCodeA])
	require.Nil(t, bindings.byTelegram[99])
}

func TestTelegramBooleanAndTypedConfirmationAreIdempotent(t *testing.T) {
	ctx := context.Background()
	settingRepo := &telegramSettingRepoStub{values: map[string]string{
		SettingKeyRegistrationEnabled: "false",
		SettingKeyDefaultConcurrency:  "2",
		SettingKeySiteName:            "Old Site",
		SettingKeyAdminAPIKey:         "old-secret",
	}}
	settings := NewSettingService(settingRepo, &config.Config{})
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bindings.byTelegram[99] = &TelegramBinding{ID: 3, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bot := &telegramBotStub{}
	service := newTelegramTestService(bindings, state, bot, settings, telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}})

	boolID := telegramDefinitionIDByKey(t, SettingKeyRegistrationEnabled)
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdate(1, 99, "b:"+boolID+":1")))
	require.NotContains(t, bot.lastText(), "old-secret")
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdate(2, 99, "ok:"+boolID)))
	require.Equal(t, "true", settingRepo.values[SettingKeyRegistrationEnabled])
	require.Equal(t, 1, settingRepo.setCalls)
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdate(3, 99, "ok:"+boolID)))
	require.Equal(t, 1, settingRepo.setCalls)

	excludedID := telegramSettingID(SettingKeyAdminAPIKey)
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdate(4, 99, "e:"+excludedID)))
	require.Contains(t, bot.lastText(), "设置已不存在")
	require.Equal(t, "old-secret", settingRepo.values[SettingKeyAdminAPIKey])
	require.Empty(t, state.pending)

	intID := telegramDefinitionIDByKey(t, SettingKeyDefaultConcurrency)
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdate(5, 99, "e:"+intID)))
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(6, 99, "not-an-int")))
	require.Contains(t, bot.lastText(), "有效整数")
	require.Equal(t, "2", settingRepo.values[SettingKeyDefaultConcurrency])

	textID := telegramDefinitionIDByKey(t, SettingKeySiteName)
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdate(7, 99, "e:"+textID)))
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(8, 99, "New Site")))
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdate(9, 99, "ok:"+textID)))
	require.Equal(t, "New Site", settingRepo.values[SettingKeySiteName])
	require.NotContains(t, strings.Join(bot.texts, "\n"), "old-secret")
}

func TestTelegramAuthorizationAndWebRevocation(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	binding := &TelegramBinding{ID: 4, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bindings.byTelegram[99] = binding
	bindings.byID[4] = binding
	users := telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}}
	service := newTelegramTestService(bindings, state, &telegramBotStub{}, nil, users)

	service.runtime.Store(&telegramRuntimeSnapshot{enabled: false, lifecycleStatus: "disabled"})
	require.NoError(t, service.RevokeBinding(ctx, 7, 4))
	require.Nil(t, bindings.byTelegram[99])

	bindings.byTelegram[99] = binding
	bindings.byID[4] = binding
	require.ErrorIs(t, service.RevokeBinding(ctx, 8, 4), ErrTelegramBindingNotFound)
	require.NotNil(t, bindings.byTelegram[99])

	users.users[7].Status = "disabled"
	require.ErrorIs(t, service.RevokeBinding(ctx, 7, 4), ErrTelegramBindingNotFound)
	require.NotNil(t, bindings.byTelegram[99])

	users.users[7].Status = StatusActive
	users.users[7].Role = RoleUser
	require.ErrorIs(t, service.RevokeBinding(ctx, 7, 4), ErrTelegramBindingNotFound)
	require.NotNil(t, bindings.byTelegram[99])

	_, _, err := service.loadAuthorizedBinding(ctx, TelegramIdentity{UserID: 99, PrivateChatID: 99})
	require.ErrorIs(t, err, ErrTelegramBindingNotFound)
}

func TestParseTelegramCallbackRejectsRawKeysAndOversizeData(t *testing.T) {
	id := telegramSettingID("registration_enabled")
	nonce := "0123456789abcdef"
	for _, data := range []string{
		"h", "g:all:0", "v:" + id, "b:" + id + ":1", "ok:" + id, "no:" + id, "qp:2",
		"gm", "gm:n", "gm:p:openai", "gm:l:openai:4", "gm:g:123:4", "gm:i:123:base:4", "gm:ok:123:base:" + nonce, "gm:no:123:base:4:" + nonce,
	} {
		_, ok := parseTelegramCallback(data)
		require.True(t, ok, data)
	}
	for _, data := range []string{
		"v:registration_enabled", "b:" + id + ":true", "g:unknown:0", strings.Repeat("x", 65), "",
		"gm:p:unknown", "gm:l:openai:-1", "gm:g:0:1", "gm:i:12:unknown:0", "gm:ok:12:base:short", "gm:no:12:base:-1:" + nonce,
	} {
		_, ok := parseTelegramCallback(data)
		require.False(t, ok, data)
	}
}

func TestTelegramGroupRateFlowUsesSortedPaginationAndOneMenuMessage(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bindings.byTelegram[99] = &TelegramBinding{ID: 3, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bot := &telegramBotStub{}
	admin := newTelegramAdminStub([]Group{
		{ID: 8, Name: "Zulu", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 4, Name: "beta", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 7, Name: "Echo", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 6, Name: "delta", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 5, Name: "Charlie", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 2, Name: "Alpha", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 3, Name: "alpha", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
	})
	service := newTelegramTestService(bindings, state, bot, nil, telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}})
	service.admin = admin
	bindings.groupAdmin = admin
	service.groupRates = bindings
	audit := NewAuditLogService(nil, nil)
	service.audit = audit
	cache := &telegramAuthCacheInvalidatorStub{}
	service.authCacheInvalidator = cache

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(1, 99, 700, "gm")))
	requireTelegramCallback(t, bot.lastMarkup(), "s")
	requireTelegramCallback(t, bot.lastMarkup(), "s")

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(2, 99, 700, "gm:p:openai")))
	require.Equal(t, "sort_order", admin.lastSortBy)
	require.Equal(t, "asc", admin.lastSortOrder)
	require.Equal(t, TelegramGroupPageSize, admin.lastPageSize)
	requireTelegramCallback(t, bot.lastMarkup(), "gm:n")
	requireTelegramCallback(t, bot.lastMarkup(), "gm:l:openai:1")
	requireTelegramCallback(t, bot.lastMarkup(), "gm")
	require.Equal(t, "Alpha · 1x · 启用", bot.lastMarkup().InlineKeyboard[0][0].Text)
	require.Equal(t, "alpha · 1x · 启用", bot.lastMarkup().InlineKeyboard[1][0].Text)

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(3, 99, 700, "gm:g:2:0")))
	requireTelegramCallback(t, bot.lastMarkup(), "gm:l:openai:0")
	requireTelegramCallback(t, bot.lastMarkup(), "gm:i:2:base:0")

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(4, 99, 700, "gm:i:2:base:0")))
	pending := state.pending[99]
	require.Equal(t, telegramGroupRateFlow, pending.Flow)
	require.Equal(t, "input", pending.Stage)
	require.Equal(t, 700, pending.OriginMessageID)
	require.NotEmpty(t, pending.OperationNonce)
	requireTelegramCallback(t, bot.lastMarkup(), "gm:no:2:base:0:"+pending.OperationNonce)

	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(5, 99, "not-a-number")))
	require.Contains(t, bot.lastText(), "输入无效")
	require.Equal(t, "input", state.pending[99].Stage)
	require.Equal(t, 700, bot.lastEditMessageID)
	require.Zero(t, bot.sendCount)

	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(6, 99, "1.75")))
	require.Equal(t, "confirm", state.pending[99].Stage)
	require.Equal(t, "1.75", state.pending[99].Candidate)
	nonce := state.pending[99].OperationNonce
	requireTelegramCallback(t, bot.lastMarkup(), "gm:ok:2:base:"+nonce)
	requireTelegramCallback(t, bot.lastMarkup(), "gm:i:2:base:0")
	requireTelegramCallback(t, bot.lastMarkup(), "gm:no:2:base:0:"+nonce)
	require.Equal(t, []telegramDeletedMessage{{chatID: 99, messageID: 5}, {chatID: 99, messageID: 6}}, bot.deleted)

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(7, 99, 700, "gm:ok:2:base:"+nonce)))
	require.Equal(t, 1.75, admin.groups[2].RateMultiplier)
	require.Equal(t, 1, admin.updateCalls)
	require.Empty(t, state.pending)
	require.Zero(t, bot.sendCount)
	require.Equal(t, 7, bot.editCount)
	require.Equal(t, 700, bot.lastEditMessageID)
	requireTelegramCallback(t, bot.lastMarkup(), "gm:g:2:0")
	requireTelegramCallback(t, bot.lastMarkup(), "gm:l:openai:0")
	require.Equal(t, []int64{2}, cache.groupIDs)
	select {
	case entry := <-audit.queue:
		require.Equal(t, "admin.telegram.group.rate.update", entry.Action)
		require.Equal(t, "telegram", entry.AuthMethod)
		require.Equal(t, "TELEGRAM", entry.Method)
		require.Equal(t, "telegram://admin", entry.Path)
		require.Equal(t, int64(7), *entry.ActorUserID)
		require.Equal(t, int64(3), entry.Extra["binding_id"])
		require.Equal(t, int64(99), entry.Extra["telegram_user_id"])
		require.Equal(t, int64(2), entry.Extra["group_id"])
		require.Equal(t, "Alpha", entry.Extra["group_name"])
		require.Equal(t, PlatformOpenAI, entry.Extra["platform"])
		require.Equal(t, TelegramGroupRateKindBase, entry.Extra["rate_kind"])
		require.Equal(t, 1.0, entry.Extra["before"])
		require.Equal(t, 1.75, entry.Extra["after"])
	default:
		t.Fatal("missing Telegram group-rate audit entry")
	}
}

func TestTelegramGroupRateCancelEditsOriginAndClearsPending(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bindings.byTelegram[99] = &TelegramBinding{ID: 3, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bot := &telegramBotStub{}
	service := newTelegramTestService(bindings, state, bot, nil, telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}})
	service.admin = newTelegramAdminStub([]Group{{ID: 2, Name: "Alpha", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1}})

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(1, 99, 700, "gm:i:2:base:0")))
	require.NotEmpty(t, state.pending)
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(2, 99, "/cancel")))
	require.Empty(t, state.pending)
	require.Zero(t, bot.sendCount)
	require.Equal(t, 700, bot.lastEditMessageID)
	require.Contains(t, bot.lastText(), "已取消")
}

func TestTelegramGroupRateNavigationClearsOnlyItsPendingState(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bindings.byTelegram[99] = &TelegramBinding{ID: 3, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	service := newTelegramTestService(bindings, state, &telegramBotStub{}, nil, telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}})
	service.admin = newTelegramAdminStub([]Group{{ID: 2, Name: "Alpha", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1}})

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(1, 99, 700, "gm:i:2:base:0")))
	require.NotEmpty(t, state.pending)
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(2, 99, 700, "gm")))
	require.Empty(t, state.pending)

	state.pending[99] = TelegramPendingSettingInput{Flow: "", SettingKey: "site.name", InputType: TelegramSettingTypeText, Stage: "input"}
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(3, 99, 700, "gm")))
	require.Contains(t, state.pending, int64(99))
}

func TestTelegramGroupRateConfirmRequiresLiveBinding(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bindings.byTelegram[99] = &TelegramBinding{ID: 3, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bot := &telegramBotStub{}
	admin := newTelegramAdminStub([]Group{{ID: 2, Name: "Alpha", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1}})
	service := newTelegramTestService(bindings, state, bot, nil, telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}})
	service.admin = admin
	bindings.groupAdmin = admin
	service.groupRates = bindings

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(1, 99, 700, "gm:i:2:base:0")))
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(2, 99, "1.5")))
	nonce := state.pending[99].OperationNonce
	delete(bindings.byTelegram, 99)
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(3, 99, 700, "gm:ok:2:base:"+nonce)))
	require.Zero(t, admin.updateCalls)
	require.Equal(t, 1.0, admin.groups[2].RateMultiplier)
	require.Contains(t, bot.lastText(), "绑定已失效")
	requireTelegramCallback(t, bot.lastMarkup(), "s")
	require.Empty(t, state.pending)
}

func TestTelegramGroupRateStaleAndDuplicateConfirmCannotMutate(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bindings.byTelegram[99] = &TelegramBinding{ID: 3, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bot := &telegramBotStub{}
	admin := newTelegramAdminStub([]Group{{ID: 2, Name: "Alpha", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1}})
	service := newTelegramTestService(bindings, state, bot, nil, telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}})
	service.admin = admin
	service.groupRates = bindings
	bindings.groupAdmin = admin

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(1, 99, 700, "gm:i:2:base:0")))
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(2, 99, "1.5")))
	staleNonce := state.pending[99].OperationNonce

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(3, 99, 700, "gm:i:2:base:0")))
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(4, 99, "2")))
	currentNonce := state.pending[99].OperationNonce
	require.NotEqual(t, staleNonce, currentNonce)

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(5, 99, 700, "gm:ok:2:base:"+staleNonce)))
	require.Equal(t, 1.0, admin.groups[2].RateMultiplier)
	require.Equal(t, currentNonce, state.pending[99].OperationNonce)
	require.Zero(t, admin.updateCalls)

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(6, 99, 701, "gm:ok:2:base:"+currentNonce)))
	require.Equal(t, currentNonce, state.pending[99].OperationNonce)
	require.Zero(t, admin.updateCalls)

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(7, 99, 700, "gm:ok:2:base:"+currentNonce)))
	require.Equal(t, 2.0, admin.groups[2].RateMultiplier)
	require.Equal(t, 1, admin.updateCalls)
	require.Empty(t, state.pending)

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(8, 99, 700, "gm:ok:2:base:"+currentNonce)))
	require.Equal(t, 1, admin.updateCalls)
}

func TestTelegramGroupRateCommandNavigationEditsOriginAndClearsPending(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bindings.byTelegram[99] = &TelegramBinding{ID: 3, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bot := &telegramBotStub{}
	service := newTelegramTestService(bindings, state, bot, nil, telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}})
	service.admin = newTelegramAdminStub([]Group{{ID: 2, Name: "Alpha", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1}})

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(1, 99, 700, "gm:i:2:base:0")))
	require.NoError(t, service.ProcessUpdate(ctx, telegramTextUpdate(2, 99, "/help")))
	require.Empty(t, state.pending)
	require.Zero(t, bot.sendCount)
	require.Equal(t, 700, bot.lastEditMessageID)
	require.Contains(t, bot.lastText(), "/start")
	require.Contains(t, bot.deleted, telegramDeletedMessage{chatID: 99, messageID: 2})
}

func TestTelegramGroupRateCurrentPageButtonIsNoop(t *testing.T) {
	ctx := context.Background()
	state := newTelegramStateStub()
	bindings := newTelegramBindingStub()
	bindings.byTelegram[99] = &TelegramBinding{ID: 3, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bot := &telegramBotStub{}
	admin := newTelegramAdminStub([]Group{
		{ID: 1, SortOrder: 1, Name: "One", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 2, SortOrder: 2, Name: "Two", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 3, SortOrder: 3, Name: "Three", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 4, SortOrder: 4, Name: "Four", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 5, SortOrder: 5, Name: "Five", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 6, SortOrder: 6, Name: "Six", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
		{ID: 7, SortOrder: 7, Name: "Seven", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1},
	})
	service := newTelegramTestService(bindings, state, bot, nil, telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}})
	service.admin = admin

	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(1, 99, 700, "gm:p:openai")))
	before := bot.editCount
	requireTelegramCallback(t, bot.lastMarkup(), "gm:n")
	require.NoError(t, service.ProcessUpdate(ctx, telegramCallbackUpdateWithMessage(2, 99, 700, "gm:n")))
	require.Equal(t, before, bot.editCount)
}

func TestTelegramPaginationRowsOffersBoundedPageJumps(t *testing.T) {
	rows := telegramPaginationRows(5, 12, func(page int) string { return "page:" + strconv.Itoa(page) })
	require.Len(t, rows, 2)
	require.Len(t, rows[0], 2)
	require.Len(t, rows[1], 5)
	require.Equal(t, "page:4", rows[0][0].CallbackData)
	require.Equal(t, "page:6", rows[0][1].CallbackData)
	require.Equal(t, "page:3", rows[1][0].CallbackData)
	require.Equal(t, "· 6 ·", rows[1][2].Text)
	require.Equal(t, "gm:n", rows[1][2].CallbackData)
	require.Equal(t, "page:7", rows[1][4].CallbackData)
}

func TestParseTelegramMultiplierBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		kind    string
		want    float64
		wantErr bool
	}{
		{name: "base positive", raw: "1.25", kind: TelegramGroupRateKindBase, want: 1.25},
		{name: "base zero", raw: "0", kind: TelegramGroupRateKindBase, wantErr: true},
		{name: "base negative", raw: "-1", kind: TelegramGroupRateKindBase, wantErr: true},
		{name: "optional zero", raw: "0", kind: TelegramGroupRateKindImage, want: 0},
		{name: "optional negative", raw: "-0.1", kind: TelegramGroupRateKindPeak, wantErr: true},
		{name: "maximum", raw: "1000000", kind: TelegramGroupRateKindVideo, want: 1_000_000},
		{name: "above maximum", raw: "1000000.0001", kind: TelegramGroupRateKindVideo, wantErr: true},
		{name: "nan", raw: "NaN", kind: TelegramGroupRateKindBase, wantErr: true},
		{name: "positive infinity", raw: "+Inf", kind: TelegramGroupRateKindBase, wantErr: true},
		{name: "negative infinity", raw: "-Inf", kind: TelegramGroupRateKindImage, wantErr: true},
		{name: "malformed", raw: "one", kind: TelegramGroupRateKindBase, wantErr: true},
		{name: "unsupported kind", raw: "1", kind: "unknown", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := parseTelegramMultiplier(test.raw, test.kind)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, value)
		})
	}
}

func TestTelegramIdentityRequiresPrivateSenderChatConsistency(t *testing.T) {
	identity, ok := telegramIdentityFromMessage(&TelegramMessage{From: &TelegramUser{ID: 42}, Chat: TelegramChat{ID: 42, Type: "private"}})
	require.True(t, ok)
	require.Equal(t, int64(42), identity.UserID)
	_, ok = telegramIdentityFromMessage(&TelegramMessage{From: &TelegramUser{ID: 42}, Chat: TelegramChat{ID: -100, Type: "group"}})
	require.False(t, ok)
}

func catalogEntryByKey(t *testing.T, entries []TelegramSettingCatalogEntry, key string) TelegramSettingCatalogEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Key == key {
			return entry
		}
	}
	t.Fatalf("missing catalog entry %s", key)
	return TelegramSettingCatalogEntry{}
}

func telegramDefinitionIDByKey(t *testing.T, key string) string {
	t.Helper()
	for _, definition := range telegramSettingDefinitions {
		if definition.Key == key {
			return definition.ID
		}
	}
	t.Fatalf("missing Telegram setting definition %s", key)
	return ""
}

func activeTelegramAdmin(id int64) *User {
	return &User{ID: id, Email: "admin@example.com", Role: RoleAdmin, Status: StatusActive}
}

func newTelegramTestService(bindings TelegramAdminBindingRepository, state TelegramStateRepository, bot TelegramBotAPI, settings *SettingService, users TelegramAdminUserReader) *TelegramBotService {
	cfg := &config.Config{}
	cfg.Telegram.Enabled = true
	cfg.Telegram.BotToken = "test-token"
	cfg.Telegram.BotUsername = "test_bot"
	return NewTelegramBotService(cfg, bindings, state, bot, settings, users, nil, nil, nil, nil)
}

func telegramTextUpdate(updateID, userID int64, text string) *TelegramUpdate {
	return &TelegramUpdate{UpdateID: updateID, Message: &TelegramMessage{MessageID: int(updateID), From: &TelegramUser{ID: userID}, Chat: TelegramChat{ID: userID, Type: "private"}, Text: text}}
}

func telegramCallbackUpdate(updateID, userID int64, data string) *TelegramUpdate {
	return telegramCallbackUpdateWithMessage(updateID, userID, int(updateID), data)
}

func telegramCallbackUpdateWithMessage(updateID, userID int64, messageID int, data string) *TelegramUpdate {
	return &TelegramUpdate{UpdateID: updateID, CallbackQuery: &TelegramCallbackQuery{ID: "callback", From: TelegramUser{ID: userID}, Data: data, Message: &TelegramMessage{MessageID: messageID, Chat: TelegramChat{ID: userID, Type: "private"}}}}
}

type telegramUserReaderStub struct{ users map[int64]*User }

func (s telegramUserReaderStub) GetByID(_ context.Context, id int64) (*User, error) {
	user := s.users[id]
	if user == nil {
		return nil, errors.New("not found")
	}
	return user, nil
}

type telegramSettingRepoStub struct {
	values   map[string]string
	setCalls int
}

func (r *telegramSettingRepoStub) Get(_ context.Context, key string) (*Setting, error) {
	return &Setting{Key: key, Value: r.values[key]}, nil
}
func (r *telegramSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}
func (r *telegramSettingRepoStub) Set(_ context.Context, key, value string) error {
	if _, ok := r.values[key]; !ok {
		return errors.New("not found")
	}
	r.values[key] = value
	r.setCalls++
	return nil
}
func (r *telegramSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}
func (r *telegramSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		r.values[key] = value
	}
	r.setCalls++
	return nil
}
func (r *telegramSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	result := map[string]string{}
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}
func (r *telegramSettingRepoStub) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

type telegramBindingStub struct {
	byTelegram map[int64]*TelegramBinding
	byID       map[int64]*TelegramBinding
	nextID     int64
	lookupErr  error
	bindErr    error
	groupAdmin *telegramAdminStub
}

func newTelegramBindingStub() *telegramBindingStub {
	return &telegramBindingStub{byTelegram: map[int64]*TelegramBinding{}, byID: map[int64]*TelegramBinding{}, nextID: 10}
}
func (r *telegramBindingStub) Bind(_ context.Context, adminUserID int64, identity TelegramIdentity) (*TelegramBinding, error) {
	if r.bindErr != nil {
		return nil, r.bindErr
	}
	r.nextID++
	binding := &TelegramBinding{ID: r.nextID, AdminUserID: adminUserID, TelegramUserID: identity.UserID, PrivateChatID: identity.PrivateChatID}
	r.byTelegram[identity.UserID] = binding
	r.byID[binding.ID] = binding
	return binding, nil
}
func (r *telegramBindingStub) ListActiveBindings(_ context.Context, adminUserID int64) ([]*TelegramBinding, error) {
	var result []*TelegramBinding
	for _, binding := range r.byTelegram {
		if binding.AdminUserID == adminUserID {
			result = append(result, binding)
		}
	}
	return result, nil
}
func (r *telegramBindingStub) GetActiveBindingByTelegramUserID(_ context.Context, id int64) (*TelegramBinding, error) {
	if r.lookupErr != nil {
		return nil, r.lookupErr
	}
	binding := r.byTelegram[id]
	if binding == nil {
		return nil, ErrTelegramBindingNotFound
	}
	return binding, nil
}
func (r *telegramBindingStub) RevokeBinding(_ context.Context, bindingID, adminUserID int64) (*TelegramBinding, error) {
	binding := r.byID[bindingID]
	if binding == nil || binding.AdminUserID != adminUserID {
		return nil, ErrTelegramBindingNotFound
	}
	delete(r.byID, bindingID)
	delete(r.byTelegram, binding.TelegramUserID)
	return binding, nil
}

func (r *telegramBindingStub) UpdateAuthorizedGroupRateMultiplier(_ context.Context, bindingID int64, identity TelegramIdentity, groupID int64, kind string, multiplier float64, expectedUpdatedAt time.Time) (*TelegramGroupRateMutation, error) {
	binding := r.byTelegram[identity.UserID]
	if binding == nil || binding.ID != bindingID || binding.PrivateChatID != identity.PrivateChatID || r.groupAdmin == nil {
		return nil, ErrTelegramBindingNotFound
	}
	group := r.groupAdmin.groups[groupID]
	if group == nil {
		return nil, ErrGroupNotFound
	}
	if !group.UpdatedAt.Equal(expectedUpdatedAt) {
		return nil, ErrTelegramGroupChanged
	}
	before, ok := findTelegramGroupRateOption(group, kind)
	if !ok {
		return nil, ErrTelegramPendingInputInvalid
	}
	switch kind {
	case TelegramGroupRateKindBase:
		group.RateMultiplier = multiplier
	case TelegramGroupRateKindImage:
		group.ImageRateMultiplier = multiplier
	case TelegramGroupRateKindVideo:
		group.VideoRateMultiplier = multiplier
	case TelegramGroupRateKindPeak:
		group.PeakRateMultiplier = multiplier
	}
	r.groupAdmin.updateCalls++
	copy := *group
	return &TelegramGroupRateMutation{
		Binding: binding,
		Admin:   activeTelegramAdmin(binding.AdminUserID),
		Group:   &copy,
		Kind:    kind,
		Before:  before.Value,
		After:   multiplier,
	}, nil
}

type telegramStateStub struct {
	codes    map[string]int64
	attempts map[int64]int
	claimed  map[int64]bool
	pending  map[int64]TelegramPendingSettingInput
}

func newTelegramStateStub() *telegramStateStub {
	return &telegramStateStub{
		codes:    map[string]int64{},
		attempts: map[int64]int{},
		claimed:  map[int64]bool{},
		pending:  map[int64]TelegramPendingSettingInput{},
	}
}
func (r *telegramStateStub) IssueVerificationCode(context.Context, int64) (*TelegramVerificationCode, error) {
	return nil, errors.New("unused")
}
func (r *telegramStateStub) GetVerificationCodeStatus(context.Context, int64) (*TelegramVerificationCodeStatus, error) {
	return nil, nil
}
func (r *telegramStateStub) CancelVerificationCode(context.Context, int64) (bool, error) {
	return false, nil
}
func (r *telegramStateStub) ConsumeVerificationCode(_ context.Context, code string) (int64, time.Duration, error) {
	id, ok := r.codes[code]
	if !ok {
		return 0, 0, ErrTelegramVerificationCodeInvalid
	}
	delete(r.codes, code)
	return id, TelegramVerificationCodeTTL, nil
}
func (r *telegramStateStub) RestoreVerificationCode(_ context.Context, code string, adminUserID int64, _ time.Duration) (bool, error) {
	if _, exists := r.codes[code]; exists {
		return false, nil
	}
	r.codes[code] = adminUserID
	return true, nil
}
func (r *telegramStateStub) AllowVerificationAttempt(_ context.Context, id int64) (bool, error) {
	r.attempts[id]++
	return r.attempts[id] <= TelegramVerificationAttemptLimit, nil
}
func (r *telegramStateStub) ClearVerificationAttempts(_ context.Context, id int64) error {
	delete(r.attempts, id)
	return nil
}
func (r *telegramStateStub) ClaimUpdate(_ context.Context, id int64) (bool, error) {
	if r.claimed[id] {
		return false, nil
	}
	r.claimed[id] = true
	return true, nil
}
func (r *telegramStateStub) CompleteUpdate(context.Context, int64) error { return nil }
func (r *telegramStateStub) ReleaseUpdate(_ context.Context, id int64) error {
	delete(r.claimed, id)
	return nil
}
func (r *telegramStateStub) AcquireConfigLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (r *telegramStateStub) ReleaseConfigLock(context.Context, string) error { return nil }
func (r *telegramStateStub) SetPendingSettingInput(_ context.Context, id int64, input TelegramPendingSettingInput) error {
	r.pending[id] = input
	return nil
}
func (r *telegramStateStub) GetPendingSettingInput(_ context.Context, id int64) (*TelegramPendingSettingInput, error) {
	input, ok := r.pending[id]
	if !ok {
		return nil, nil
	}
	return &input, nil
}
func (r *telegramStateStub) TakePendingSettingInput(_ context.Context, id int64) (*TelegramPendingSettingInput, error) {
	input, ok := r.pending[id]
	if !ok {
		return nil, nil
	}
	delete(r.pending, id)
	return &input, nil
}
func (r *telegramStateStub) TakePendingSettingInputIfNonce(_ context.Context, id int64, nonce string) (*TelegramPendingSettingInput, error) {
	input, ok := r.pending[id]
	if !ok || input.OperationNonce != nonce {
		return nil, nil
	}
	delete(r.pending, id)
	return &input, nil
}
func (r *telegramStateStub) DeletePendingSettingInput(_ context.Context, id int64) (bool, error) {
	_, ok := r.pending[id]
	delete(r.pending, id)
	return ok, nil
}

type telegramDeletedMessage struct {
	chatID    int64
	messageID int
}

type telegramAdminStub struct {
	AdminService
	groups        map[int64]*Group
	lastPage      int
	lastPageSize  int
	lastPlatform  string
	lastSortBy    string
	lastSortOrder string
	updateCalls   int
}

func newTelegramAdminStub(groups []Group) *telegramAdminStub {
	result := &telegramAdminStub{groups: make(map[int64]*Group, len(groups))}
	for index := range groups {
		group := groups[index]
		result.groups[group.ID] = &group
	}
	return result
}

func (s *telegramAdminStub) ListGroups(_ context.Context, page, pageSize int, platform, _, _ string, _ *bool, sortBy, sortOrder string) ([]Group, int64, error) {
	s.lastPage, s.lastPageSize, s.lastPlatform = page, pageSize, platform
	s.lastSortBy, s.lastSortOrder = sortBy, sortOrder
	groups := make([]Group, 0, len(s.groups))
	for _, group := range s.groups {
		if platform == "" || group.Platform == platform {
			groups = append(groups, *group)
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].SortOrder == groups[j].SortOrder {
			return groups[i].ID < groups[j].ID
		}
		return groups[i].SortOrder < groups[j].SortOrder
	})
	start := (page - 1) * pageSize
	if start >= len(groups) {
		return []Group{}, int64(len(groups)), nil
	}
	end := start + pageSize
	if end > len(groups) {
		end = len(groups)
	}
	return groups[start:end], int64(len(groups)), nil
}

func (s *telegramAdminStub) GetGroup(_ context.Context, id int64) (*Group, error) {
	group := s.groups[id]
	if group == nil {
		return nil, errors.New("group not found")
	}
	copy := *group
	return &copy, nil
}

type telegramAuthCacheInvalidatorStub struct{ groupIDs []int64 }

func (*telegramAuthCacheInvalidatorStub) InvalidateAuthCacheByKey(context.Context, string)   {}
func (*telegramAuthCacheInvalidatorStub) InvalidateAuthCacheByUserID(context.Context, int64) {}
func (s *telegramAuthCacheInvalidatorStub) InvalidateAuthCacheByGroupID(_ context.Context, id int64) {
	s.groupIDs = append(s.groupIDs, id)
}

func requireTelegramCallback(t *testing.T, markup *TelegramInlineKeyboardMarkup, callback string) {
	t.Helper()
	require.NotNil(t, markup)
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == callback {
				return
			}
		}
	}
	t.Fatalf("missing Telegram callback %q", callback)
}

type telegramBotStub struct {
	texts             []string
	deleted           []telegramDeletedMessage
	deleteErr         error
	sendErr           error
	sendCount         int
	editCount         int
	lastEditChatID    int64
	lastEditMessageID int
	markups           []*TelegramInlineKeyboardMarkup
}

func (b *telegramBotStub) GetMe(context.Context) (*TelegramUser, error) {
	return &TelegramUser{ID: 1, IsBot: true, Username: "test_bot"}, nil
}
func (b *telegramBotStub) SetMyCommands(context.Context, []TelegramBotCommand) error { return nil }
func (b *telegramBotStub) SetChatMenuButton(context.Context) error                   { return nil }
func (b *telegramBotStub) SetWebhook(context.Context, string, string, []string) error {
	return nil
}
func (b *telegramBotStub) DeleteWebhook(context.Context, bool) error { return nil }
func (b *telegramBotStub) SendMessage(_ context.Context, _ int64, text string, markup *TelegramInlineKeyboardMarkup) error {
	if b.sendErr != nil {
		return b.sendErr
	}
	b.texts = append(b.texts, text)
	b.markups = append(b.markups, markup)
	b.sendCount++
	return nil
}
func (b *telegramBotStub) EditMessageText(_ context.Context, chatID int64, messageID int, text string, markup *TelegramInlineKeyboardMarkup) error {
	b.texts = append(b.texts, text)
	b.markups = append(b.markups, markup)
	b.editCount++
	b.lastEditChatID = chatID
	b.lastEditMessageID = messageID
	return nil
}
func (b *telegramBotStub) DeleteMessage(_ context.Context, chatID int64, messageID int) error {
	b.deleted = append(b.deleted, telegramDeletedMessage{chatID: chatID, messageID: messageID})
	return b.deleteErr
}
func (b *telegramBotStub) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (b *telegramBotStub) lastText() string {
	if len(b.texts) == 0 {
		return ""
	}
	return b.texts[len(b.texts)-1]
}

func (b *telegramBotStub) lastMarkup() *TelegramInlineKeyboardMarkup {
	if len(b.markups) == 0 {
		return nil
	}
	return b.markups[len(b.markups)-1]
}
