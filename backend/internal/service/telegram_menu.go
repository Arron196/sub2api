package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const telegramSettingsPageSize = 7

var telegramSettingCategories = []struct {
	Code  string
	Label string
}{
	{"site", "站点与注册"}, {"feature", "功能开关"}, {"security", "安全与登录"},
	{"gateway", "网关与调度"}, {"notify", "通知"}, {"payment", "支付"},
	{"oauth", "OAuth/外部登录"}, {"advanced", "运维与高级"}, {"all", "全部设置"},
}

func (s *TelegramBotService) processMessage(ctx context.Context, message *TelegramMessage) error {
	identity, ok := telegramIdentityFromMessage(message)
	if !ok {
		return nil
	}
	text := strings.TrimSpace(message.Text)
	command, args, isCommand := parseTelegramCommand(text, s.botUsernameFromContext(ctx))
	binding, user, authErr := s.loadAuthorizedBinding(ctx, identity)
	if authErr != nil {
		if !errors.Is(authErr, ErrTelegramBindingNotFound) {
			return authErr
		}
		code := text
		if isCommand && command == "bind" && len(args) == 1 {
			code = args[0]
		}
		if resemblesTelegramVerificationCode(code) {
			allowed, err := s.state.AllowVerificationAttempt(ctx, identity.UserID)
			if err != nil {
				return err
			}
			if !allowed {
				return s.send(ctx, identity.PrivateChatID, "尝试次数过多，请稍后再试。", nil)
			}
			bound, err := s.ConsumeVerificationCode(ctx, code, identity)
			if err != nil {
				return s.send(ctx, identity.PrivateChatID, "验证码无效或已过期，请在网页后台重新生成。", nil)
			}
			admin, err := s.loadActiveAdmin(ctx, bound.AdminUserID)
			if err != nil {
				return s.send(ctx, identity.PrivateChatID, "验证码无效或已过期，请在网页后台重新生成。", nil)
			}
			return s.renderHome(ctx, identity.PrivateChatID, 0, admin, "绑定成功")
		}
		return s.send(ctx, identity.PrivateChatID, "请先在网页后台生成 15 位字母数字验证码，然后直接发送给机器人。", nil)
	}
	_ = binding

	if isCommand {
		pending, err := s.state.GetPendingSettingInput(ctx, identity.UserID)
		if err != nil {
			return err
		}
		if pending != nil && pending.Flow == telegramGroupRateFlow {
			if pending.OperationNonce != "" {
				_, err = s.state.TakePendingSettingInputIfNonce(ctx, identity.UserID, pending.OperationNonce)
			} else {
				_, err = s.state.DeletePendingSettingInput(ctx, identity.UserID)
			}
			if err != nil {
				return err
			}
			var renderErr error
			switch command {
			case "start":
				renderErr = s.renderHome(ctx, pending.OriginChatID, pending.OriginMessageID, user, "")
			case "settings":
				renderErr = s.renderSettingsRoot(ctx, pending.OriginChatID, pending.OriginMessageID)
			case "status":
				renderErr = s.renderStatus(ctx, pending.OriginChatID, pending.OriginMessageID)
			case "help":
				renderErr = s.editOrSend(ctx, pending.OriginChatID, pending.OriginMessageID, telegramHelpText, homeKeyboard())
			case "cancel":
				renderErr = s.editOrSend(ctx, pending.OriginChatID, pending.OriginMessageID, "已取消当前操作\n\n管理菜单", homeKeyboard())
			case "bind":
				renderErr = s.editOrSend(ctx, pending.OriginChatID, pending.OriginMessageID, "当前 Telegram 已绑定。", homeKeyboard())
			default:
				renderErr = s.editOrSend(ctx, pending.OriginChatID, pending.OriginMessageID, "不支持该命令，请使用 /help。", homeKeyboard())
			}
			if renderErr == nil && message.MessageID > 0 {
				if bot := s.botFromContext(ctx); bot != nil {
					_ = bot.DeleteMessage(ctx, message.Chat.ID, message.MessageID)
				}
			}
			return renderErr
		}
		switch command {
		case "start":
			return s.renderHome(ctx, identity.PrivateChatID, 0, user, "")
		case "settings":
			return s.renderSettingsRoot(ctx, identity.PrivateChatID, 0)
		case "status":
			return s.renderStatus(ctx, identity.PrivateChatID, 0)
		case "help":
			return s.send(ctx, identity.PrivateChatID, telegramHelpText, homeKeyboard())
		case "cancel":
			_, _ = s.state.DeletePendingSettingInput(ctx, identity.UserID)
			return s.renderHome(ctx, identity.PrivateChatID, 0, user, "已取消当前操作")
		case "bind":
			return s.send(ctx, identity.PrivateChatID, "当前 Telegram 已绑定。", homeKeyboard())
		default:
			return s.send(ctx, identity.PrivateChatID, "不支持该命令，请使用 /help。", homeKeyboard())
		}
	}

	pending, err := s.state.GetPendingSettingInput(ctx, identity.UserID)
	if err != nil {
		return err
	}
	if pending == nil {
		return s.renderHome(ctx, identity.PrivateChatID, 0, user, "请使用下方菜单选择操作")
	}
	if pending.Flow == "group_rate" {
		return s.handleGroupRateInput(ctx, identity, message, pending)
	}
	switch pending.Stage {
	case "search":
		pending.Stage = "search_results"
		pending.Candidate = truncateTelegramText(text, 120)
		pending.ExpiresAt = s.now().Add(TelegramPendingSettingTTL)
		if err := s.state.SetPendingSettingInput(ctx, identity.UserID, *pending); err != nil {
			return err
		}
		return s.renderSettingList(ctx, identity.PrivateChatID, 0, "search", 0, pending.Candidate)
	case "input":
		entry, err := s.settings.ResolveTelegramSetting(ctx, pending.SettingID)
		if err != nil || entry.Key != pending.SettingKey || entry.Type != pending.InputType {
			_, _ = s.state.DeletePendingSettingInput(ctx, identity.UserID)
			return s.send(ctx, identity.PrivateChatID, "设置已变化，请重新选择。", homeKeyboard())
		}
		candidate, err := validateTelegramSettingValue(entry, text)
		if err != nil {
			return s.send(ctx, identity.PrivateChatID, telegramInputError(entry.Type), cancelKeyboard())
		}
		pending.Stage = "confirm"
		pending.Candidate = candidate
		pending.ExpiresAt = s.now().Add(TelegramPendingSettingTTL)
		if err := s.state.SetPendingSettingInput(ctx, identity.UserID, *pending); err != nil {
			return err
		}
		return s.sendSettingConfirmation(ctx, identity.PrivateChatID, 0, entry, candidate)
	default:
		return s.renderHome(ctx, identity.PrivateChatID, 0, user, "请先确认或取消当前修改")
	}
}

func parseTelegramCommand(text, botUsername string) (string, []string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", nil, false
	}
	command := strings.TrimPrefix(fields[0], "/")
	if command == "" || strings.Contains(command, "@") && !strings.EqualFold(strings.SplitN(command, "@", 2)[1], botUsername) {
		return "", nil, false
	}
	if strings.Contains(command, "@") {
		command = strings.SplitN(command, "@", 2)[0]
	}
	return command, fields[1:], true
}

func resemblesTelegramVerificationCode(value string) bool {
	value = normalizeTelegramVerificationCodeInput(value)
	if len(value) != TelegramVerificationCodeLength {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' && ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}

func (s *TelegramBotService) processCallback(ctx context.Context, callback *TelegramCallbackQuery) error {
	identity, ok := telegramIdentityFromCallback(callback)
	if !ok {
		return nil
	}
	binding, user, err := s.loadAuthorizedBinding(ctx, identity)
	if err != nil {
		_ = s.clearTelegramGroupRatePending(ctx, identity.UserID)
		return s.editCallback(ctx, callback.Message, "该 Telegram 绑定已失效，请在网页后台重新绑定。", homeKeyboard())
	}
	action, ok := parseTelegramCallback(callback.Data)
	if !ok {
		return s.editCallback(ctx, callback.Message, "无效操作，请重新打开菜单。", homeKeyboard())
	}
	if clearsTelegramGroupRatePending(action.Kind) {
		if err := s.clearTelegramGroupRatePending(ctx, identity.UserID); err != nil {
			return err
		}
	}
	switch action.Kind {
	case "home", "refresh":
		return s.renderHome(ctx, identity.PrivateChatID, callback.Message.MessageID, user, "")
	case "status":
		return s.renderStatus(ctx, identity.PrivateChatID, callback.Message.MessageID)
	case "settings":
		return s.renderSettingsRoot(ctx, identity.PrivateChatID, callback.Message.MessageID)
	case "group_rates":
		return s.renderGroupRatePlatforms(ctx, identity.PrivateChatID, callback.Message.MessageID)
	case "group_list":
		return s.renderGroupRateList(ctx, identity.PrivateChatID, callback.Message.MessageID, action.Arg, action.Page)
	case "group_detail":
		return s.renderGroupRateDetail(ctx, identity.PrivateChatID, callback.Message.MessageID, action.GroupID, action.Page)
	case "group_input":
		return s.beginGroupRateInput(ctx, binding, identity, callback.Message, action.GroupID, action.Arg, action.Page)
	case "group_confirm":
		return s.confirmGroupRate(ctx, identity, callback.Message, action.GroupID, action.Arg, action.Nonce)
	case "group_cancel":
		pending, err := s.state.GetPendingSettingInput(ctx, identity.UserID)
		if err != nil {
			return err
		}
		if pending == nil || pending.OperationNonce != action.Nonce || callback.Message == nil ||
			pending.OriginChatID != callback.Message.Chat.ID || pending.OriginMessageID != callback.Message.MessageID {
			return s.editCallback(ctx, callback.Message, "倍率修改请求已失效，请重新选择。", &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回平台", CallbackData: "gm"}}}})
		}
		if _, err := s.state.TakePendingSettingInputIfNonce(ctx, identity.UserID, action.Nonce); err != nil {
			return err
		}
		return s.renderGroupRateDetail(ctx, identity.PrivateChatID, callback.Message.MessageID, action.GroupID, action.Page)
	case "group_noop":
		return nil
	case "help":
		return s.editCallback(ctx, callback.Message, telegramHelpText, homeKeyboard())
	case "category":
		return s.renderSettingList(ctx, identity.PrivateChatID, callback.Message.MessageID, action.Arg, action.Page, "")
	case "search":
		return s.beginSettingSearch(ctx, identity, callback.Message)
	case "view":
		return s.renderSettingDetail(ctx, callback.Message, action.Arg)
	case "edit":
		return s.beginSettingEdit(ctx, identity, callback.Message, action.Arg)
	case "boolean":
		return s.prepareBooleanConfirmation(ctx, identity, callback.Message, action.Arg, action.Value)
	case "confirm":
		return s.confirmPendingSetting(ctx, identity, callback.Message, action.Arg)
	case "cancel":
		_, _ = s.state.DeletePendingSettingInput(ctx, identity.UserID)
		if action.Arg != "" {
			return s.renderSettingDetail(ctx, callback.Message, action.Arg)
		}
		return s.renderSettingsRoot(ctx, identity.PrivateChatID, callback.Message.MessageID)
	case "search_page":
		pending, err := s.state.GetPendingSettingInput(ctx, identity.UserID)
		if err != nil || pending == nil || pending.Stage != "search_results" {
			return s.renderSettingsRoot(ctx, identity.PrivateChatID, callback.Message.MessageID)
		}
		return s.renderSettingList(ctx, identity.PrivateChatID, callback.Message.MessageID, "search", action.Page, pending.Candidate)
	default:
		return nil
	}
}

type telegramCallbackAction struct {
	Kind    string
	Arg     string
	Page    int
	Value   bool
	GroupID int64
	Nonce   string
}

func parseTelegramCallback(data string) (telegramCallbackAction, bool) {
	if len(data) == 0 || len(data) > 64 {
		return telegramCallbackAction{}, false
	}
	switch data {
	case "h":
		return telegramCallbackAction{Kind: "home"}, true
	case "r":
		return telegramCallbackAction{Kind: "refresh"}, true
	case "st":
		return telegramCallbackAction{Kind: "status"}, true
	case "s":
		return telegramCallbackAction{Kind: "settings"}, true
	case "hp":
		return telegramCallbackAction{Kind: "help"}, true
	case "q":
		return telegramCallbackAction{Kind: "search"}, true
	case "gm":
		return telegramCallbackAction{Kind: "group_rates"}, true
	case "gm:n":
		return telegramCallbackAction{Kind: "group_noop"}, true
	case "no":
		return telegramCallbackAction{Kind: "cancel"}, true
	}
	parts := strings.Split(data, ":")
	switch {
	case len(parts) == 3 && parts[0] == "gm" && parts[1] == "p" && validTelegramPlatform(parts[2]):
		return telegramCallbackAction{Kind: "group_list", Arg: parts[2]}, true
	case len(parts) == 4 && parts[0] == "gm" && parts[1] == "l" && validTelegramPlatform(parts[2]):
		page, err := strconv.Atoi(parts[3])
		if err != nil || page < 0 {
			return telegramCallbackAction{}, false
		}
		return telegramCallbackAction{Kind: "group_list", Arg: parts[2], Page: page}, true
	case len(parts) == 4 && parts[0] == "gm" && parts[1] == "g":
		groupID, page, ok := parseTelegramGroupPage(parts[2], parts[3])
		if !ok {
			return telegramCallbackAction{}, false
		}
		return telegramCallbackAction{Kind: "group_detail", GroupID: groupID, Page: page}, true
	case len(parts) == 5 && parts[0] == "gm" && parts[1] == "i" && validTelegramGroupRateKind(parts[3]):
		groupID, page, ok := parseTelegramGroupPage(parts[2], parts[4])
		if !ok {
			return telegramCallbackAction{}, false
		}
		return telegramCallbackAction{Kind: "group_input", GroupID: groupID, Arg: parts[3], Page: page}, true
	case len(parts) == 5 && parts[0] == "gm" && parts[1] == "ok" && validTelegramGroupRateKind(parts[3]) && validTelegramOperationNonce(parts[4]):
		groupID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || groupID <= 0 {
			return telegramCallbackAction{}, false
		}
		return telegramCallbackAction{Kind: "group_confirm", GroupID: groupID, Arg: parts[3], Nonce: parts[4]}, true
	case len(parts) == 6 && parts[0] == "gm" && parts[1] == "no" && validTelegramGroupRateKind(parts[3]) && validTelegramOperationNonce(parts[5]):
		groupID, page, ok := parseTelegramGroupPage(parts[2], parts[4])
		if !ok {
			return telegramCallbackAction{}, false
		}
		return telegramCallbackAction{Kind: "group_cancel", GroupID: groupID, Arg: parts[3], Page: page, Nonce: parts[5]}, true
	case len(parts) == 3 && parts[0] == "g" && validTelegramCategory(parts[1]):
		page, err := strconv.Atoi(parts[2])
		if err != nil || page < 0 {
			return telegramCallbackAction{}, false
		}
		return telegramCallbackAction{Kind: "category", Arg: parts[1], Page: page}, true
	case len(parts) == 2 && parts[0] == "v" && validTelegramSettingID(parts[1]):
		return telegramCallbackAction{Kind: "view", Arg: parts[1]}, true
	case len(parts) == 2 && parts[0] == "e" && validTelegramSettingID(parts[1]):
		return telegramCallbackAction{Kind: "edit", Arg: parts[1]}, true
	case len(parts) == 3 && parts[0] == "b" && validTelegramSettingID(parts[1]) && (parts[2] == "0" || parts[2] == "1"):
		return telegramCallbackAction{Kind: "boolean", Arg: parts[1], Value: parts[2] == "1"}, true
	case len(parts) == 2 && parts[0] == "ok" && validTelegramSettingID(parts[1]):
		return telegramCallbackAction{Kind: "confirm", Arg: parts[1]}, true
	case len(parts) == 2 && parts[0] == "no" && validTelegramSettingID(parts[1]):
		return telegramCallbackAction{Kind: "cancel", Arg: parts[1]}, true
	case len(parts) == 2 && parts[0] == "qp":
		page, err := strconv.Atoi(parts[1])
		if err != nil || page < 0 {
			return telegramCallbackAction{}, false
		}
		return telegramCallbackAction{Kind: "search_page", Page: page}, true
	default:
		return telegramCallbackAction{}, false
	}
}

func validTelegramSettingID(id string) bool {
	if len(id) < 6 || len(id) > 16 {
		return false
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'z' {
			return false
		}
	}
	return true
}

func validTelegramCategory(code string) bool {
	for _, category := range telegramSettingCategories {
		if category.Code == code {
			return true
		}
	}
	return false
}

func validTelegramPlatform(value string) bool {
	switch value {
	case PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok:
		return true
	default:
		return false
	}
}

func validTelegramGroupRateKind(value string) bool {
	switch value {
	case TelegramGroupRateKindBase, TelegramGroupRateKindImage, TelegramGroupRateKindVideo, TelegramGroupRateKindPeak:
		return true
	default:
		return false
	}
}

func validTelegramOperationNonce(value string) bool {
	if len(value) != 16 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
			return false
		}
	}
	return true
}

func parseTelegramGroupPage(groupValue, pageValue string) (int64, int, bool) {
	groupID, err := strconv.ParseInt(groupValue, 10, 64)
	if err != nil || groupID <= 0 {
		return 0, 0, false
	}
	page, err := strconv.Atoi(pageValue)
	if err != nil || page < 0 {
		return 0, 0, false
	}
	return groupID, page, true
}

func telegramCategoryLabel(code string) string {
	for _, category := range telegramSettingCategories {
		if category.Code == code {
			return category.Label
		}
	}
	return "全部设置"
}

func telegramCategoryCode(label string) string {
	for _, category := range telegramSettingCategories {
		if category.Label == label {
			return category.Code
		}
	}
	return "advanced"
}

func (s *TelegramBotService) renderHome(ctx context.Context, chatID int64, messageID int, user *User, notice string) error {
	text := "管理菜单"
	if notice != "" {
		text = notice + "\n\n" + text
	}
	if user != nil && user.Email != "" {
		text += "\n管理员：" + truncateTelegramText(user.Email, 160)
	}
	return s.editOrSend(ctx, chatID, messageID, text, homeKeyboard())
}

func (s *TelegramBotService) renderStatus(ctx context.Context, chatID int64, messageID int) error {
	entries, err := s.settings.ListTelegramSettingCatalog(ctx)
	if err != nil {
		return err
	}
	lines := []string{fmt.Sprintf("站点状态\n可编辑设置：%d 项", len(entries))}
	for _, key := range []string{SettingKeyRegistrationEnabled, SettingKeyBackendModeEnabled, SettingKeyAvailableChannelsEnabled} {
		for _, entry := range entries {
			if entry.Key == key {
				lines = append(lines, entry.Label+"："+entry.DisplayValue)
				break
			}
		}
	}
	return s.editOrSend(ctx, chatID, messageID, strings.Join(lines, "\n"), homeKeyboard())
}

func (s *TelegramBotService) renderSettingsRoot(ctx context.Context, chatID int64, messageID int) error {
	entries, err := s.settings.ListTelegramSettingCatalog(ctx)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, entry := range entries {
		counts[telegramCategoryCode(entry.Category)]++
	}
	rows := make([][]TelegramInlineKeyboardButton, 0, 7)
	for i := 0; i < len(telegramSettingCategories); i += 2 {
		row := []TelegramInlineKeyboardButton{}
		for j := i; j < i+2 && j < len(telegramSettingCategories); j++ {
			category := telegramSettingCategories[j]
			count := counts[category.Code]
			if category.Code == "all" {
				count = len(entries)
			}
			row = append(row, TelegramInlineKeyboardButton{Text: fmt.Sprintf("%s (%d)", category.Label, count), CallbackData: "g:" + category.Code + ":0"})
		}
		rows = append(rows, row)
	}
	rows = append(rows, []TelegramInlineKeyboardButton{{Text: "搜索设置", CallbackData: "q"}})
	if s.admin != nil {
		rows = append(rows, []TelegramInlineKeyboardButton{{Text: "分组倍率", CallbackData: "gm"}})
	}
	rows = append(rows, []TelegramInlineKeyboardButton{{Text: "主页", CallbackData: "h"}})
	return s.editOrSend(ctx, chatID, messageID, "设置管理\n选择分类或搜索现有设置。", &TelegramInlineKeyboardMarkup{InlineKeyboard: rows})
}

func (s *TelegramBotService) renderSettingList(ctx context.Context, chatID int64, messageID int, category string, page int, query string) error {
	entries, err := s.settings.ListTelegramSettingCatalog(ctx)
	if err != nil {
		return err
	}
	filtered := make([]TelegramSettingCatalogEntry, 0, len(entries))
	query = strings.ToLower(strings.TrimSpace(query))
	for _, entry := range entries {
		if category == "search" {
			if query != "" && (strings.Contains(strings.ToLower(entry.Key), query) || strings.Contains(strings.ToLower(entry.Label), query)) {
				filtered = append(filtered, entry)
			}
		} else if category == "all" || telegramCategoryCode(entry.Category) == category {
			filtered = append(filtered, entry)
		}
	}
	pages := (len(filtered) + telegramSettingsPageSize - 1) / telegramSettingsPageSize
	if pages < 1 {
		pages = 1
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * telegramSettingsPageSize
	end := start + telegramSettingsPageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	rows := make([][]TelegramInlineKeyboardButton, 0, telegramSettingsPageSize+2)
	for _, entry := range filtered[start:end] {
		rows = append(rows, []TelegramInlineKeyboardButton{{Text: truncateTelegramText(entry.Label+" · "+entry.DisplayValue, 48), CallbackData: "v:" + entry.ID}})
	}
	nav := []TelegramInlineKeyboardButton{}
	pagePrefix := "g:" + category + ":"
	if category == "search" {
		pagePrefix = "qp:"
	}
	if page > 0 {
		nav = append(nav, TelegramInlineKeyboardButton{Text: "上一页", CallbackData: pagePrefix + strconv.Itoa(page-1)})
	}
	if page+1 < pages {
		nav = append(nav, TelegramInlineKeyboardButton{Text: "下一页", CallbackData: pagePrefix + strconv.Itoa(page+1)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []TelegramInlineKeyboardButton{{Text: "返回分类", CallbackData: "s"}, {Text: "主页", CallbackData: "h"}})
	title := telegramCategoryLabel(category)
	if category == "search" {
		title = "搜索结果"
	}
	text := fmt.Sprintf("%s\n第 %d/%d 页，共 %d 项", title, page+1, pages, len(filtered))
	return s.editOrSend(ctx, chatID, messageID, text, &TelegramInlineKeyboardMarkup{InlineKeyboard: rows})
}

func (s *TelegramBotService) renderSettingDetail(ctx context.Context, message *TelegramMessage, id string) error {
	entry, err := s.settings.ResolveTelegramSetting(ctx, id)
	if err != nil {
		return s.editCallback(ctx, message, "设置已不存在，请刷新目录。", settingsBackKeyboard())
	}
	text := fmt.Sprintf("%s\n键：%s\n分类：%s\n类型：%s\n当前值：%s", entry.Label, entry.Key, entry.Category, entry.Type, entry.DisplayValue)
	markup := &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{
		{{Text: "修改", CallbackData: "e:" + entry.ID}},
		{{Text: "返回设置", CallbackData: "s"}, {Text: "主页", CallbackData: "h"}},
	}}
	return s.editCallback(ctx, message, text, markup)
}

func (s *TelegramBotService) beginSettingSearch(ctx context.Context, identity TelegramIdentity, message *TelegramMessage) error {
	pending := TelegramPendingSettingInput{SettingKey: "__search__", InputType: TelegramSettingTypeText, Stage: "search", ExpiresAt: s.now().Add(TelegramPendingSettingTTL)}
	if err := s.state.SetPendingSettingInput(ctx, identity.UserID, pending); err != nil {
		return err
	}
	return s.editCallback(ctx, message, "请输入设置名称或键名进行搜索。", cancelKeyboard())
}

func (s *TelegramBotService) beginSettingEdit(ctx context.Context, identity TelegramIdentity, message *TelegramMessage, id string) error {
	entry, err := s.settings.ResolveTelegramSetting(ctx, id)
	if err != nil {
		return s.editCallback(ctx, message, "设置已不存在，请刷新目录。", settingsBackKeyboard())
	}
	if entry.Type == TelegramSettingTypeBool {
		markup := &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{
			{{Text: "启用", CallbackData: "b:" + id + ":1"}, {Text: "禁用", CallbackData: "b:" + id + ":0"}},
			{{Text: "取消", CallbackData: "no:" + id}},
		}}
		return s.editCallback(ctx, message, entry.Label+"\n请选择目标状态。", markup)
	}
	pending := TelegramPendingSettingInput{SettingKey: entry.Key, SettingID: entry.ID, InputType: entry.Type, Stage: "input", ExpiresAt: s.now().Add(TelegramPendingSettingTTL)}
	if err := s.state.SetPendingSettingInput(ctx, identity.UserID, pending); err != nil {
		return err
	}
	instruction := fmt.Sprintf("修改 %s\n请发送新的 %s 值。", entry.Label, entry.Type)
	if entry.Category == "运维与高级" {
		instruction += "\n此项影响运行行为，提交后仍需再次确认。"
	}
	return s.editCallback(ctx, message, instruction, cancelKeyboard())
}

func (s *TelegramBotService) prepareBooleanConfirmation(ctx context.Context, identity TelegramIdentity, message *TelegramMessage, id string, target bool) error {
	entry, err := s.settings.ResolveTelegramSetting(ctx, id)
	if err != nil || entry.Type != TelegramSettingTypeBool {
		return s.editCallback(ctx, message, "设置已变化，请重新选择。", settingsBackKeyboard())
	}
	candidate := strconv.FormatBool(target)
	pending := TelegramPendingSettingInput{SettingKey: entry.Key, SettingID: entry.ID, InputType: entry.Type, Stage: "confirm", Candidate: candidate, ExpiresAt: s.now().Add(TelegramPendingSettingTTL)}
	if err := s.state.SetPendingSettingInput(ctx, identity.UserID, pending); err != nil {
		return err
	}
	return s.sendSettingConfirmation(ctx, identity.PrivateChatID, message.MessageID, entry, candidate)
}

func (s *TelegramBotService) sendSettingConfirmation(ctx context.Context, chatID int64, messageID int, entry TelegramSettingCatalogEntry, candidate string) error {
	after := telegramSafeSettingValue(candidate)
	text := fmt.Sprintf("确认修改\n%s\n当前值：%s\n修改后：%s", entry.Label, entry.DisplayValue, after)
	if entry.Category == "运维与高级" {
		text += "\n请确认已了解此修改可能影响站点运行。"
	}
	markup := &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{
		{Text: "确认修改", CallbackData: "ok:" + entry.ID}, {Text: "取消", CallbackData: "no:" + entry.ID},
	}}}
	return s.editOrSend(ctx, chatID, messageID, text, markup)
}

func (s *TelegramBotService) confirmPendingSetting(ctx context.Context, identity TelegramIdentity, message *TelegramMessage, id string) error {
	binding, admin, err := s.loadAuthorizedBinding(ctx, identity)
	if err != nil {
		return s.editCallback(ctx, message, "该 Telegram 绑定已失效，请重新绑定。", nil)
	}
	pending, err := s.state.GetPendingSettingInput(ctx, identity.UserID)
	if err != nil {
		return err
	}
	if pending == nil || pending.Stage != "confirm" || pending.SettingID != id {
		return s.editCallback(ctx, message, "修改请求已失效，请重新选择。", settingsBackKeyboard())
	}
	entry, err := s.settings.ResolveTelegramSetting(ctx, id)
	if err != nil || entry.Key != pending.SettingKey || entry.Type != pending.InputType {
		_, _ = s.state.DeletePendingSettingInput(ctx, identity.UserID)
		return s.editCallback(ctx, message, "设置已变化，请重新选择。", settingsBackKeyboard())
	}
	result, err := s.settings.UpdateTelegramSetting(ctx, id, pending.Candidate)
	if err != nil {
		return err
	}
	_, _ = s.state.TakePendingSettingInput(ctx, identity.UserID)
	if result.Changed {
		extra := map[string]any{"key": entry.Key, "type": entry.Type, "telegram_user_id": binding.TelegramUserID, "changed": true}
		extra["before"] = result.Previous
		extra["after"] = result.Entry.RawValue
		s.recordAudit(admin, "admin.telegram.setting.update", "telegram", extra)
	}
	notice := "设置未变化"
	if result.Changed {
		notice = "设置已更新"
	}
	return s.editCallback(ctx, message, notice+"\n\n"+entry.Label+"："+result.Entry.DisplayValue, settingsBackKeyboard())
}

func telegramInputError(valueType TelegramSettingInputType) string {
	switch valueType {
	case TelegramSettingTypeInt:
		return "请输入有效整数，或使用 /cancel 取消。"
	case TelegramSettingTypeFloat:
		return "请输入有效小数，或使用 /cancel 取消。"
	case TelegramSettingTypeJSON:
		return "请输入有效的 JSON 对象或数组，或使用 /cancel 取消。"
	default:
		return "输入无效或过长，请重试或使用 /cancel 取消。"
	}
}

func (s *TelegramBotService) editOrSend(ctx context.Context, chatID int64, messageID int, text string, markup *TelegramInlineKeyboardMarkup) error {
	bot := s.botFromContext(ctx)
	if bot == nil {
		return ErrTelegramUnavailable
	}
	if messageID > 0 {
		return bot.EditMessageText(ctx, chatID, messageID, text, markup)
	}
	return bot.SendMessage(ctx, chatID, text, markup)
}

func (s *TelegramBotService) editCallback(ctx context.Context, message *TelegramMessage, text string, markup *TelegramInlineKeyboardMarkup) error {
	if message == nil {
		return nil
	}
	bot := s.botFromContext(ctx)
	if bot == nil {
		return ErrTelegramUnavailable
	}
	return bot.EditMessageText(ctx, message.Chat.ID, message.MessageID, text, markup)
}

func (s *TelegramBotService) send(ctx context.Context, chatID int64, text string, markup *TelegramInlineKeyboardMarkup) error {
	bot := s.botFromContext(ctx)
	if bot == nil {
		return ErrTelegramUnavailable
	}
	return bot.SendMessage(ctx, chatID, text, markup)
}

func homeKeyboard() *TelegramInlineKeyboardMarkup {
	return &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{
		{{Text: "站点状态", CallbackData: "st"}, {Text: "设置管理", CallbackData: "s"}},
		{{Text: "刷新", CallbackData: "r"}, {Text: "帮助", CallbackData: "hp"}},
	}}
}

func settingsBackKeyboard() *TelegramInlineKeyboardMarkup {
	return &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "返回设置", CallbackData: "s"}, {Text: "主页", CallbackData: "h"}}}}
}

func cancelKeyboard() *TelegramInlineKeyboardMarkup {
	return &TelegramInlineKeyboardMarkup{InlineKeyboard: [][]TelegramInlineKeyboardButton{{{Text: "取消", CallbackData: "no"}}}}
}

const telegramHelpText = "可用命令：\n/start 打开管理菜单\n/bind ABC12DEF34GHI56 绑定验证码\n/settings 管理站点设置\n/status 查看站点状态\n/help 查看帮助\n/cancel 取消当前操作"
