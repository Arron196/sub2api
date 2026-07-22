package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const telegramGroupRateFlow = "group_rate"

type telegramGroupRateOption struct {
	Kind  string
	Label string
	Value float64
}

var telegramGroupPlatforms = []struct {
	Code  string
	Label string
}{
	{PlatformAnthropic, "Anthropic"},
	{PlatformOpenAI, "OpenAI"},
	{PlatformGemini, "Gemini"},
	{PlatformAntigravity, "Antigravity"},
	{PlatformGrok, "Grok"},
}

func supportsTelegramImageRate(platform string) bool {
	switch platform {
	case PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok:
		return true
	default:
		return false
	}
}

func clearsTelegramGroupRatePending(action string) bool {
	switch action {
	case "home", "refresh", "status", "settings", "help", "group_rates", "group_list", "group_detail":
		return true
	default:
		return false
	}
}

func (s *TelegramBotService) clearTelegramGroupRatePending(ctx context.Context, telegramUserID int64) error {
	pending, err := s.state.GetPendingSettingInput(ctx, telegramUserID)
	if err != nil || pending == nil || pending.Flow != telegramGroupRateFlow {
		return err
	}
	_, err = s.state.DeletePendingSettingInput(ctx, telegramUserID)
	return err
}

func telegramGroupRateOptions(group *Group) []telegramGroupRateOption {
	if group == nil {
		return nil
	}
	options := []telegramGroupRateOption{{Kind: TelegramGroupRateKindBase, Label: "基础计费倍率", Value: group.RateMultiplier}}
	if supportsTelegramImageRate(group.Platform) && group.ImageRateIndependent {
		options = append(options, telegramGroupRateOption{Kind: TelegramGroupRateKindImage, Label: "独立图片倍率", Value: group.ImageRateMultiplier})
	}
	if group.Platform == PlatformGrok && group.VideoRateIndependent {
		options = append(options, telegramGroupRateOption{Kind: TelegramGroupRateKindVideo, Label: "独立视频倍率", Value: group.VideoRateMultiplier})
	}
	if group.IsSubscriptionType() && group.PeakRateEnabled {
		options = append(options, telegramGroupRateOption{Kind: TelegramGroupRateKindPeak, Label: "高峰时段倍率", Value: group.PeakRateMultiplier})
	}
	return options
}

func findTelegramGroupRateOption(group *Group, kind string) (telegramGroupRateOption, bool) {
	for _, option := range telegramGroupRateOptions(group) {
		if option.Kind == kind {
			return option, true
		}
	}
	return telegramGroupRateOption{}, false
}

func (s *TelegramBotService) renderGroupRatePlatforms(ctx context.Context, chatID int64, messageID int) error {
	if s.admin == nil {
		return s.editOrSend(ctx, chatID, messageID, "分组倍率管理暂不可用。", settingsBackKeyboard())
	}
	rows := make([][]TelegramInlineKeyboardButton, 0, 4)
	for i := 0; i < len(telegramGroupPlatforms); i += 2 {
		row := make([]TelegramInlineKeyboardButton, 0, 2)
		for j := i; j < i+2 && j < len(telegramGroupPlatforms); j++ {
			platform := telegramGroupPlatforms[j]
			row = append(row, TelegramInlineKeyboardButton{Text: platform.Label, CallbackData: "gm:p:" + platform.Code})
		}
		rows = append(rows, row)
	}
	rows = append(rows, []TelegramInlineKeyboardButton{{Text: "返回设置", CallbackData: "s"}, {Text: "主页", CallbackData: "h"}})
	return s.editOrSend(ctx, chatID, messageID, "分组倍率\n请选择平台。分组将按后台排序和 ID 稳定排列。", &TelegramInlineKeyboardMarkup{InlineKeyboard: rows})
}

func (s *TelegramBotService) renderGroupRateList(ctx context.Context, chatID int64, messageID int, platform string, page int) error {
	if s.admin == nil || !validTelegramPlatform(platform) {
		return s.editOrSend(ctx, chatID, messageID, "分组倍率管理暂不可用。", settingsBackKeyboard())
	}
	groups, total, err := s.admin.ListGroups(ctx, page+1, TelegramGroupPageSize, platform, "", "", nil, "sort_order", "asc")
	if err != nil {
		return err
	}
	totalPages := int((total + TelegramGroupPageSize - 1) / TelegramGroupPageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page >= totalPages {
		page = totalPages - 1
		groups, _, err = s.admin.ListGroups(ctx, page+1, TelegramGroupPageSize, platform, "", "", nil, "sort_order", "asc")
		if err != nil {
			return err
		}
	}
	rows := make([][]TelegramInlineKeyboardButton, 0, len(groups)+4)
	for _, group := range groups {
		status := "启用"
		if !group.IsActive() {
			status = "停用"
		}
		label := fmt.Sprintf("%s · %sx · %s", group.Name, formatTelegramMultiplier(group.RateMultiplier), status)
		rows = append(rows, []TelegramInlineKeyboardButton{{
			Text:         truncateTelegramText(label, 48),
			CallbackData: fmt.Sprintf("gm:g:%d:%d", group.ID, page),
		}})
	}
	rows = append(rows, telegramPaginationRows(page, totalPages, func(target int) string {
		return fmt.Sprintf("gm:l:%s:%d", platform, target)
	})...)
	rows = append(rows, []TelegramInlineKeyboardButton{{Text: "返回平台", CallbackData: "gm"}, {Text: "返回设置", CallbackData: "s"}})
	text := fmt.Sprintf("%s 分组\n第 %d/%d 页，共 %d 个\n排序：后台排序，顺序相同时按 ID 升序", telegramPlatformLabel(platform), page+1, totalPages, total)
	if len(groups) == 0 {
		text += "\n当前平台没有分组。"
	}
	return s.editOrSend(ctx, chatID, messageID, text, &TelegramInlineKeyboardMarkup{InlineKeyboard: rows})
}

func telegramPaginationRows(page, totalPages int, callback func(int) string) [][]TelegramInlineKeyboardButton {
	if totalPages <= 1 {
		return nil
	}
	rows := make([][]TelegramInlineKeyboardButton, 0, 2)
	navigation := make([]TelegramInlineKeyboardButton, 0, 2)
	if page > 0 {
		navigation = append(navigation, TelegramInlineKeyboardButton{Text: "上一页", CallbackData: callback(page - 1)})
	}
	if page+1 < totalPages {
		navigation = append(navigation, TelegramInlineKeyboardButton{Text: "下一页", CallbackData: callback(page + 1)})
	}
	if len(navigation) > 0 {
		rows = append(rows, navigation)
	}
	start := page - 2
	if start < 0 {
		start = 0
	}
	end := start + 5
	if end > totalPages {
		end = totalPages
		start = end - 5
		if start < 0 {
			start = 0
		}
	}
	pages := make([]TelegramInlineKeyboardButton, 0, end-start)
	for index := start; index < end; index++ {
		label := strconv.Itoa(index + 1)
		if index == page {
			label = "· " + label + " ·"
		}
		callbackData := callback(index)
		if index == page {
			callbackData = "gm:n"
		}
		pages = append(pages, TelegramInlineKeyboardButton{Text: label, CallbackData: callbackData})
	}
	return append(rows, pages)
}

func (s *TelegramBotService) renderGroupRateDetail(ctx context.Context, chatID int64, messageID int, groupID int64, page int) error {
	if s.admin == nil {
		return s.editOrSend(ctx, chatID, messageID, "分组倍率管理暂不可用。", settingsBackKeyboard())
	}
	group, err := s.admin.GetGroup(ctx, groupID)
	if err != nil {
		return s.editOrSend(ctx, chatID, messageID, "分组不存在或已被删除。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回平台", CallbackData: "gm"}, {Text: "返回设置", CallbackData: "s"}}}})
	}
	options := telegramGroupRateOptions(group)
	lines := []string{
		"分组倍率",
		"分组：" + truncateTelegramText(group.Name, 120),
		"平台：" + telegramPlatformLabel(group.Platform),
		"状态：" + group.Status,
	}
	rows := make([][]TelegramInlineKeyboardButton, 0, len(options)+2)
	for _, option := range options {
		lines = append(lines, option.Label+"："+formatTelegramMultiplier(option.Value)+"x")
		rows = append(rows, []TelegramInlineKeyboardButton{{
			Text:         "修改" + option.Label,
			CallbackData: fmt.Sprintf("gm:i:%d:%s:%d", group.ID, option.Kind, page),
		}})
	}
	rows = append(rows, []TelegramInlineKeyboardButton{{Text: "返回分组列表", CallbackData: fmt.Sprintf("gm:l:%s:%d", group.Platform, page)}, {Text: "返回平台", CallbackData: "gm"}})
	return s.editOrSend(ctx, chatID, messageID, strings.Join(lines, "\n"), &TelegramInlineKeyboardMarkup{InlineKeyboard: rows})
}

func (s *TelegramBotService) beginGroupRateInput(ctx context.Context, binding *TelegramBinding, identity TelegramIdentity, message *TelegramMessage, groupID int64, kind string, page int) error {
	if s.admin == nil || message == nil {
		return ErrTelegramUnavailable
	}
	group, err := s.admin.GetGroup(ctx, groupID)
	if err != nil {
		return s.editCallback(ctx, message, "分组不存在或已被删除。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回平台", CallbackData: "gm"}}}})
	}
	option, ok := findTelegramGroupRateOption(group, kind)
	if !ok {
		return s.editCallback(ctx, message, "该倍率当前不可修改。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回分组", CallbackData: fmt.Sprintf("gm:g:%d:%d", group.ID, page)}}}})
	}
	if binding == nil || binding.ID <= 0 || binding.AdminUserID <= 0 {
		return ErrTelegramBindingNotFound
	}
	nonce, err := newTelegramOperationNonce()
	if err != nil {
		return err
	}
	pending := TelegramPendingSettingInput{
		Flow: telegramGroupRateFlow, SettingKey: "__group_rate__", InputType: TelegramSettingTypeFloat,
		Stage: "input", GroupID: group.ID, RateKind: kind, ReturnPage: page,
		Category: group.Platform, OriginChatID: identity.PrivateChatID, OriginMessageID: message.MessageID,
		OperationNonce: nonce, BindingID: binding.ID, AdminUserID: binding.AdminUserID, GroupUpdatedAt: group.UpdatedAt,
		ExpiresAt: s.now().Add(TelegramPendingSettingTTL),
	}
	if err := s.state.SetPendingSettingInput(ctx, identity.UserID, pending); err != nil {
		return err
	}
	text := fmt.Sprintf("修改分组倍率\n分组：%s\n类型：%s\n当前值：%sx\n\n请直接发送新的倍率数值。", truncateTelegramText(group.Name, 120), option.Label, formatTelegramMultiplier(option.Value))
	return s.editCallback(ctx, message, text, groupRateInputKeyboard(group.ID, kind, page, nonce))
}

func (s *TelegramBotService) handleGroupRateInput(ctx context.Context, identity TelegramIdentity, message *TelegramMessage, pending *TelegramPendingSettingInput) error {
	if s.admin == nil || pending == nil || pending.Stage != "input" || pending.GroupID <= 0 || !validTelegramGroupRateKind(pending.RateKind) {
		_, _ = s.state.DeletePendingSettingInput(ctx, identity.UserID)
		return s.send(ctx, identity.PrivateChatID, "倍率修改请求已失效，请重新选择。", homeKeyboard())
	}
	group, err := s.admin.GetGroup(ctx, pending.GroupID)
	if err != nil {
		_, _ = s.state.DeletePendingSettingInput(ctx, identity.UserID)
		return s.editGroupRateOrigin(ctx, message, pending, "分组不存在或已被删除。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回平台", CallbackData: "gm"}}}})
	}
	option, ok := findTelegramGroupRateOption(group, pending.RateKind)
	value, valueErr := parseTelegramMultiplier(message.Text, pending.RateKind)
	if !ok || valueErr != nil {
		text := "输入无效。基础倍率必须大于 0；其他倍率必须大于或等于 0。请重新输入数字。"
		return s.editGroupRateOrigin(ctx, message, pending, text, groupRateInputKeyboard(group.ID, pending.RateKind, pending.ReturnPage, pending.OperationNonce))
	}
	pending.Stage = "confirm"
	pending.Candidate = formatTelegramMultiplier(value)
	pending.GroupUpdatedAt = group.UpdatedAt
	pending.ExpiresAt = s.now().Add(TelegramPendingSettingTTL)
	if err := s.state.SetPendingSettingInput(ctx, identity.UserID, *pending); err != nil {
		return err
	}
	text := fmt.Sprintf("确认修改\n分组：%s\n类型：%s\n当前值：%sx\n修改后：%sx", truncateTelegramText(group.Name, 120), option.Label, formatTelegramMultiplier(option.Value), pending.Candidate)
	keyboard := &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{
		{{Text: "确认修改", CallbackData: fmt.Sprintf("gm:ok:%d:%s:%s", group.ID, pending.RateKind, pending.OperationNonce)}},
		{{Text: "返回重新输入", CallbackData: fmt.Sprintf("gm:i:%d:%s:%d", group.ID, pending.RateKind, pending.ReturnPage)}},
		{{Text: "返回分组", CallbackData: fmt.Sprintf("gm:no:%d:%s:%d:%s", group.ID, pending.RateKind, pending.ReturnPage, pending.OperationNonce)}},
	}}
	return s.editGroupRateOrigin(ctx, message, pending, text, keyboard)
}

func (s *TelegramBotService) confirmGroupRate(ctx context.Context, identity TelegramIdentity, message *TelegramMessage, groupID int64, kind, nonce string) error {
	if s.groupRates == nil {
		return ErrTelegramUnavailable
	}
	pending, err := s.state.GetPendingSettingInput(ctx, identity.UserID)
	if err != nil {
		return err
	}
	if pending == nil || pending.Flow != telegramGroupRateFlow || pending.Stage != "confirm" || pending.GroupID != groupID || pending.RateKind != kind ||
		pending.OperationNonce != nonce || message == nil || message.Chat.ID != pending.OriginChatID || message.MessageID != pending.OriginMessageID {
		return s.editCallback(ctx, message, "倍率修改请求已失效，请重新选择。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回平台", CallbackData: "gm"}, {Text: "返回设置", CallbackData: "s"}}}})
	}
	pending, err = s.state.TakePendingSettingInputIfNonce(ctx, identity.UserID, nonce)
	if err != nil {
		return err
	}
	if pending == nil {
		return s.editCallback(ctx, message, "倍率修改请求已失效，请重新选择。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回平台", CallbackData: "gm"}, {Text: "返回设置", CallbackData: "s"}}}})
	}
	value, err := parseTelegramMultiplier(pending.Candidate, kind)
	if err != nil {
		return s.editCallback(ctx, message, "倍率输入已失效，请重新选择。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回分组", CallbackData: fmt.Sprintf("gm:g:%d:%d", groupID, pending.ReturnPage)}}}})
	}
	mutation, err := s.groupRates.UpdateAuthorizedGroupRateMultiplier(ctx, pending.BindingID, identity, groupID, kind, value, pending.GroupUpdatedAt)
	if err != nil {
		if errors.Is(err, ErrTelegramBindingNotFound) {
			return s.editCallback(ctx, message, "该 Telegram 绑定已失效，请在网页后台重新绑定。", homeKeyboard())
		}
		if errors.Is(err, ErrTelegramGroupChanged) {
			return s.editCallback(ctx, message, "分组已被其他管理员修改，请返回后重新确认。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回分组", CallbackData: fmt.Sprintf("gm:g:%d:%d", groupID, pending.ReturnPage)}}}})
		}
		return s.editCallback(ctx, message, "倍率修改失败，请返回后重试。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回分组", CallbackData: fmt.Sprintf("gm:g:%d:%d", groupID, pending.ReturnPage)}}}})
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, mutation.Group.ID)
	}
	s.recordAudit(mutation.Admin, "admin.telegram.group.rate.update", "telegram", map[string]any{
		"binding_id": mutation.Binding.ID, "telegram_user_id": mutation.Binding.TelegramUserID,
		"group_id": mutation.Group.ID, "group_name": mutation.Group.Name, "platform": mutation.Group.Platform,
		"rate_kind": kind, "before": mutation.Before, "after": mutation.After,
	})
	option, _ := findTelegramGroupRateOption(mutation.Group, kind)
	text := fmt.Sprintf("倍率已更新\n分组：%s\n类型：%s\n修改前：%sx\n修改后：%sx", truncateTelegramText(mutation.Group.Name, 120), option.Label, formatTelegramMultiplier(mutation.Before), formatTelegramMultiplier(mutation.After))
	keyboard := &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{
		{{Text: "返回分组", CallbackData: fmt.Sprintf("gm:g:%d:%d", mutation.Group.ID, pending.ReturnPage)}},
		{{Text: "返回分组列表", CallbackData: fmt.Sprintf("gm:l:%s:%d", mutation.Group.Platform, pending.ReturnPage)}, {Text: "主页", CallbackData: "h"}},
	}}
	return s.editCallback(ctx, message, text, keyboard)
}

func (s *TelegramBotService) editGroupRateOrigin(ctx context.Context, incoming *TelegramMessage, pending *TelegramPendingSettingInput, text string, markup *TelegramInlineKeyboardMarkup) error {
	chatID, messageID := int64(0), 0
	if pending != nil {
		chatID, messageID = pending.OriginChatID, pending.OriginMessageID
	}
	if chatID <= 0 || messageID <= 0 {
		if incoming == nil {
			return ErrTelegramUnavailable
		}
		return s.send(ctx, incoming.Chat.ID, text, markup)
	}
	if err := s.editOrSend(ctx, chatID, messageID, text, markup); err != nil {
		return err
	}
	if incoming != nil && incoming.MessageID > 0 {
		if bot := s.botFromContext(ctx); bot != nil {
			_ = bot.DeleteMessage(ctx, incoming.Chat.ID, incoming.MessageID)
		}
	}
	return nil
}

func groupRateInputKeyboard(groupID int64, kind string, page int, nonce string) *TelegramInlineKeyboardMarkup {
	return &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{
		{{Text: "返回分组", CallbackData: fmt.Sprintf("gm:no:%d:%s:%d:%s", groupID, kind, page, nonce)}},
		{{Text: "返回平台", CallbackData: "gm"}, {Text: "主页", CallbackData: "h"}},
	}}
}

func newTelegramOperationNonce() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate Telegram operation nonce: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func parseTelegramMultiplier(raw, kind string) (float64, error) {
	if !validTelegramGroupRateKind(kind) {
		return 0, fmt.Errorf("invalid multiplier kind")
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value > 1_000_000 {
		return 0, fmt.Errorf("invalid multiplier")
	}
	if (kind == TelegramGroupRateKindBase && value <= 0) || (kind != TelegramGroupRateKindBase && value < 0) {
		return 0, fmt.Errorf("invalid multiplier")
	}
	return value, nil
}

func formatTelegramMultiplier(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func telegramPlatformLabel(platform string) string {
	for _, candidate := range telegramGroupPlatforms {
		if candidate.Code == platform {
			return candidate.Label
		}
	}
	return platform
}
