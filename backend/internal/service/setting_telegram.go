package service

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const telegramSettingMaxInputBytes = 8 * 1024

type TelegramSettingCatalogEntry struct {
	ID           string
	Key          string
	Label        string
	Category     string
	Type         TelegramSettingInputType
	DisplayValue string
	RawValue     string
}

type TelegramSettingUpdateResult struct {
	Entry    TelegramSettingCatalogEntry
	Changed  bool
	Previous string
}

type telegramSettingDefinition struct {
	ID       string
	Key      string
	Label    string
	Category string
	Type     TelegramSettingInputType
	Read     func(*SystemSettings) string
	Validate func(string) (string, error)
	Apply    func(*SystemSettings, string) error
}

// telegramSettingDefinitions is the complete Telegram settings permission
// boundary. Database rows are never added to this list implicitly.
var telegramSettingDefinitions = []telegramSettingDefinition{
	telegramBoolSetting("regopen", SettingKeyRegistrationEnabled, "开放注册", "站点与注册", func(s *SystemSettings) bool { return s.RegistrationEnabled }, func(s *SystemSettings, v bool) { s.RegistrationEnabled = v }),
	telegramBoolSetting("emailverify", SettingKeyEmailVerifyEnabled, "邮箱验证", "站点与注册", func(s *SystemSettings) bool { return s.EmailVerifyEnabled }, func(s *SystemSettings, v bool) { s.EmailVerifyEnabled = v }),
	telegramBoolSetting("promocodes", SettingKeyPromoCodeEnabled, "优惠码", "站点与注册", func(s *SystemSettings) bool { return s.PromoCodeEnabled }, func(s *SystemSettings, v bool) { s.PromoCodeEnabled = v }),
	telegramBoolSetting("passwordreset", SettingKeyPasswordResetEnabled, "密码重置", "站点与注册", func(s *SystemSettings) bool { return s.PasswordResetEnabled }, func(s *SystemSettings, v bool) { s.PasswordResetEnabled = v }),
	telegramBoolSetting("invitecodes", SettingKeyInvitationCodeEnabled, "邀请码注册", "站点与注册", func(s *SystemSettings) bool { return s.InvitationCodeEnabled }, func(s *SystemSettings, v bool) { s.InvitationCodeEnabled = v }),
	telegramTextSetting("sitename", SettingKeySiteName, "站点名称", "站点与注册", 120, false, func(s *SystemSettings) string { return s.SiteName }, func(s *SystemSettings, v string) { s.SiteName = v }),
	telegramTextSetting("sitesubtitle", SettingKeySiteSubtitle, "站点副标题", "站点与注册", 240, false, func(s *SystemSettings) string { return s.SiteSubtitle }, func(s *SystemSettings, v string) { s.SiteSubtitle = v }),
	telegramTextSetting("contactinfo", SettingKeyContactInfo, "联系方式", "站点与注册", 500, true, func(s *SystemSettings) string { return s.ContactInfo }, func(s *SystemSettings, v string) { s.ContactInfo = v }),
	telegramURLSetting("docurl", SettingKeyDocURL, "文档链接", "站点与注册", 2048, true, func(s *SystemSettings) string { return s.DocURL }, func(s *SystemSettings, v string) { s.DocURL = v }),

	telegramBoolSetting("hideccs", SettingKeyHideCcsImportButton, "隐藏 CCS 导入", "功能开关", func(s *SystemSettings) bool { return s.HideCcsImportButton }, func(s *SystemSettings, v bool) { s.HideCcsImportButton = v }),
	telegramBoolSetting("buysubtoggle", SettingKeyPurchaseSubscriptionEnabled, "购买订阅入口", "功能开关", func(s *SystemSettings) bool { return s.PurchaseSubscriptionEnabled }, func(s *SystemSettings, v bool) { s.PurchaseSubscriptionEnabled = v }),
	telegramBoolSetting("availablech", SettingKeyAvailableChannelsEnabled, "可用渠道", "功能开关", func(s *SystemSettings) bool { return s.AvailableChannelsEnabled }, func(s *SystemSettings, v bool) { s.AvailableChannelsEnabled = v }),
	telegramBoolSetting("affiliate", SettingKeyAffiliateEnabled, "邀请返利", "功能开关", func(s *SystemSettings) bool { return s.AffiliateEnabled }, func(s *SystemSettings, v bool) { s.AffiliateEnabled = v }),
	telegramBoolSetting("affadminrebate", SettingKeyAffiliateAdminRechargeEnabled, "管理员充值返利", "功能开关", func(s *SystemSettings) bool { return s.AdminRechargeRebateEnabled }, func(s *SystemSettings, v bool) { s.AdminRechargeRebateEnabled = v }),
	telegramBoolSetting("usererrors", SettingKeyAllowUserViewErrorRequests, "用户查看失败请求", "功能开关", func(s *SystemSettings) bool { return s.AllowUserViewErrorRequests }, func(s *SystemSettings, v bool) { s.AllowUserViewErrorRequests = v }),
	telegramBoolSetting("modelfallback", SettingKeyEnableModelFallback, "模型兜底", "功能开关", func(s *SystemSettings) bool { return s.EnableModelFallback }, func(s *SystemSettings, v bool) { s.EnableModelFallback = v }),
	telegramBoolSetting("identitypatch", SettingKeyEnableIdentityPatch, "身份补丁", "功能开关", func(s *SystemSettings) bool { return s.EnableIdentityPatch }, func(s *SystemSettings, v bool) { s.EnableIdentityPatch = v }),

	telegramIntSetting("tablepagesize", SettingKeyTableDefaultPageSize, "表格默认分页", "运维与高级", 5, 1000, func(s *SystemSettings) int { return s.TableDefaultPageSize }, func(s *SystemSettings, v int) { s.TableDefaultPageSize = v }),
	telegramIntSetting("defaultconcur", SettingKeyDefaultConcurrency, "默认并发量", "运维与高级", 1, 1000, func(s *SystemSettings) int { return s.DefaultConcurrency }, func(s *SystemSettings, v int) { s.DefaultConcurrency = v }),
	telegramIntSetting("defaultrpm", SettingKeyDefaultUserRPMLimit, "默认用户 RPM", "运维与高级", 0, 1000000, func(s *SystemSettings) int { return s.DefaultUserRPMLimit }, func(s *SystemSettings, v int) { s.DefaultUserRPMLimit = v }),
	telegramFloatSetting("affrebaterate", SettingKeyAffiliateRebateRate, "返利比例", "运维与高级", 0, 100, func(s *SystemSettings) float64 { return s.AffiliateRebateRate }, func(s *SystemSettings, v float64) { s.AffiliateRebateRate = v }),
	telegramIntSetting("afffreeze", SettingKeyAffiliateRebateFreezeHours, "返利冻结期", "运维与高级", 0, AffiliateRebateFreezeHoursMax, func(s *SystemSettings) int { return s.AffiliateRebateFreezeHours }, func(s *SystemSettings, v int) { s.AffiliateRebateFreezeHours = v }),
	telegramIntSetting("affduration", SettingKeyAffiliateRebateDurationDays, "返利有效期", "运维与高级", 0, AffiliateRebateDurationDaysMax, func(s *SystemSettings) int { return s.AffiliateRebateDurationDays }, func(s *SystemSettings, v int) { s.AffiliateRebateDurationDays = v }),
	telegramEnumSetting("opsquerymode", SettingKeyOpsQueryModeDefault, "运维查询模式", "运维与高级", []string{"auto", "raw", "preagg"}, func(s *SystemSettings) string { return s.OpsQueryModeDefault }, func(s *SystemSettings, v string) { s.OpsQueryModeDefault = v }),
	telegramIntSetting("opsinterval", SettingKeyOpsMetricsIntervalSeconds, "运维指标间隔", "运维与高级", 60, 3600, func(s *SystemSettings) int { return s.OpsMetricsIntervalSeconds }, func(s *SystemSettings, v int) { s.OpsMetricsIntervalSeconds = v }),
	telegramIntSetting("channelinterval", SettingKeyChannelMonitorDefaultIntervalSeconds, "渠道监控间隔", "运维与高级", 15, 3600, func(s *SystemSettings) int { return s.ChannelMonitorDefaultIntervalSeconds }, func(s *SystemSettings, v int) { s.ChannelMonitorDefaultIntervalSeconds = v }),
	telegramTextSetting("fallbackclaude", SettingKeyFallbackModelAnthropic, "Claude 兜底模型", "网关与调度", 128, false, func(s *SystemSettings) string { return s.FallbackModelAnthropic }, func(s *SystemSettings, v string) { s.FallbackModelAnthropic = v }),
	telegramTextSetting("fallbackopenai", SettingKeyFallbackModelOpenAI, "OpenAI 兜底模型", "网关与调度", 128, false, func(s *SystemSettings) string { return s.FallbackModelOpenAI }, func(s *SystemSettings, v string) { s.FallbackModelOpenAI = v }),
	telegramTextSetting("fallbackgemini", SettingKeyFallbackModelGemini, "Gemini 兜底模型", "网关与调度", 128, false, func(s *SystemSettings) string { return s.FallbackModelGemini }, func(s *SystemSettings, v string) { s.FallbackModelGemini = v }),
	telegramTextSetting("fallbackag", SettingKeyFallbackModelAntigravity, "Antigravity 兜底模型", "网关与调度", 128, false, func(s *SystemSettings) string { return s.FallbackModelAntigravity }, func(s *SystemSettings, v string) { s.FallbackModelAntigravity = v }),

	telegramBoolSetting("opsmonitor", SettingKeyOpsMonitoringEnabled, "运维监控", "网关与调度", func(s *SystemSettings) bool { return s.OpsMonitoringEnabled }, func(s *SystemSettings, v bool) { s.OpsMonitoringEnabled = v }),
	telegramBoolSetting("opsrealtime", SettingKeyOpsRealtimeMonitoringEnabled, "实时运维监控", "网关与调度", func(s *SystemSettings) bool { return s.OpsRealtimeMonitoringEnabled }, func(s *SystemSettings, v bool) { s.OpsRealtimeMonitoringEnabled = v }),
	telegramBoolSetting("channelmonitor", SettingKeyChannelMonitorEnabled, "渠道监控", "网关与调度", func(s *SystemSettings) bool { return s.ChannelMonitorEnabled }, func(s *SystemSettings, v bool) { s.ChannelMonitorEnabled = v }),
	telegramBoolSetting("backendmode", SettingKeyBackendModeEnabled, "Backend 模式", "网关与调度", func(s *SystemSettings) bool { return s.BackendModeEnabled }, func(s *SystemSettings, v bool) { s.BackendModeEnabled = v }),
	telegramBoolSetting("openailowrate", SettingKeyOpenAILowUpstreamRatePriorityEnabled, "OpenAI 上游倍率优先", "网关与调度", func(s *SystemSettings) bool { return s.OpenAILowUpstreamRatePriorityEnabled }, func(s *SystemSettings, v bool) { s.OpenAILowUpstreamRatePriorityEnabled = v }),
	telegramFloatSetting("openairatemult", SettingKeyOpenAIOAuthSchedulingRateMultiplier, "OpenAI OAuth 调度倍率", "网关与调度", 0, 100, func(s *SystemSettings) float64 { return s.OpenAIOAuthSchedulingRateMultiplier }, func(s *SystemSettings, v float64) { s.OpenAIOAuthSchedulingRateMultiplier = v }),
	telegramBoolSetting("openaiadv", openAIAdvancedSchedulerSettingKey, "OpenAI 高级调度", "网关与调度", func(s *SystemSettings) bool { return s.OpenAIAdvancedSchedulerEnabled }, func(s *SystemSettings, v bool) { s.OpenAIAdvancedSchedulerEnabled = v }),
	telegramBoolSetting("openaisticky", SettingKeyOpenAIAdvancedSchedulerStickyWeightedEnabled, "OpenAI 粘性加权", "网关与调度", func(s *SystemSettings) bool { return s.OpenAIAdvancedSchedulerStickyWeightedEnabled }, func(s *SystemSettings, v bool) { s.OpenAIAdvancedSchedulerStickyWeightedEnabled = v }),
	telegramBoolSetting("openaiprio", SettingKeyOpenAIAdvancedSchedulerSubscriptionPriorityEnabled, "OpenAI 订阅优先", "网关与调度", func(s *SystemSettings) bool { return s.OpenAIAdvancedSchedulerSubscriptionPriorityEnabled }, func(s *SystemSettings, v bool) { s.OpenAIAdvancedSchedulerSubscriptionPriorityEnabled = v }),
	telegramBoolSetting("fingerprint", SettingKeyEnableFingerprintUnification, "统一上游指纹", "网关与调度", func(s *SystemSettings) bool { return s.EnableFingerprintUnification }, func(s *SystemSettings, v bool) { s.EnableFingerprintUnification = v }),
	telegramBoolSetting("cachettl1h", SettingKeyEnableAnthropicCacheTTL1hInjection, "Anthropic 1 小时缓存", "网关与调度", func(s *SystemSettings) bool { return s.EnableAnthropicCacheTTL1hInjection }, func(s *SystemSettings, v bool) { s.EnableAnthropicCacheTTL1hInjection = v }),
	telegramBoolSetting("cachecontrol", SettingKeyRewriteMessageCacheControl, "改写缓存控制", "网关与调度", func(s *SystemSettings) bool { return s.RewriteMessageCacheControl }, func(s *SystemSettings, v bool) { s.RewriteMessageCacheControl = v }),
	telegramBoolSetting("dateline", SettingKeyEnableClientDatelineNormalization, "客户端日期归一化", "网关与调度", func(s *SystemSettings) bool { return s.EnableClientDatelineNormalization }, func(s *SystemSettings, v bool) { s.EnableClientDatelineNormalization = v }),
}

func (s *SettingService) ListTelegramSettingCatalog(ctx context.Context) ([]TelegramSettingCatalogEntry, error) {
	if s == nil || s.settingRepo == nil {
		return nil, ErrTelegramUnavailable
	}
	settings, err := s.GetAllSettings(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]TelegramSettingCatalogEntry, 0, len(telegramSettingDefinitions))
	for _, definition := range telegramSettingDefinitions {
		entries = append(entries, telegramCatalogEntry(definition, settings))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Category != entries[j].Category {
			return entries[i].Category < entries[j].Category
		}
		return entries[i].Key < entries[j].Key
	})
	return entries, nil
}

func (s *SettingService) ResolveTelegramSetting(ctx context.Context, id string) (TelegramSettingCatalogEntry, error) {
	definition, ok := telegramSettingDefinitionByID(id)
	if !ok {
		return TelegramSettingCatalogEntry{}, infraerrors.BadRequest("INVALID_TELEGRAM_SETTING", "setting is not available in Telegram")
	}
	if s == nil || s.settingRepo == nil {
		return TelegramSettingCatalogEntry{}, ErrTelegramUnavailable
	}
	settings, err := s.GetAllSettings(ctx)
	if err != nil {
		return TelegramSettingCatalogEntry{}, err
	}
	return telegramCatalogEntry(definition, settings), nil
}

// UpdateTelegramSetting is the only Telegram settings write path. It starts
// from the parsed full settings object so UpdateSettings preserves unrelated
// values and runs its normal validation, cache refresh, and onUpdate callback.
func (s *SettingService) UpdateTelegramSetting(ctx context.Context, id, candidate string) (TelegramSettingUpdateResult, error) {
	definition, ok := telegramSettingDefinitionByID(id)
	if !ok {
		return TelegramSettingUpdateResult{}, infraerrors.BadRequest("INVALID_TELEGRAM_SETTING", "setting is not available in Telegram")
	}
	validated, err := validateTelegramSettingValueForDefinition(definition, candidate)
	if err != nil {
		return TelegramSettingUpdateResult{}, err
	}
	if s == nil || s.settingRepo == nil {
		return TelegramSettingUpdateResult{}, ErrTelegramUnavailable
	}
	settings, err := s.GetAllSettings(ctx)
	if err != nil {
		return TelegramSettingUpdateResult{}, err
	}
	previous := definition.Read(settings)
	result := TelegramSettingUpdateResult{
		Entry:    telegramCatalogEntry(definition, settings),
		Previous: previous,
	}
	if previous == validated {
		return result, nil
	}
	if err := definition.Apply(settings, validated); err != nil {
		return TelegramSettingUpdateResult{}, err
	}
	if err := s.UpdateSettings(ctx, settings); err != nil {
		return TelegramSettingUpdateResult{}, err
	}
	result.Changed = true
	result.Entry.RawValue = definition.Read(settings)
	result.Entry.DisplayValue = telegramSafeSettingValue(result.Entry.RawValue)
	return result, nil
}

func telegramSettingDefinitionByID(id string) (telegramSettingDefinition, bool) {
	var match telegramSettingDefinition
	found := false
	for _, definition := range telegramSettingDefinitions {
		if definition.ID != id {
			continue
		}
		if found {
			return telegramSettingDefinition{}, false
		}
		match, found = definition, true
	}
	return match, found
}

func telegramCatalogEntry(definition telegramSettingDefinition, settings *SystemSettings) TelegramSettingCatalogEntry {
	value := definition.Read(settings)
	return TelegramSettingCatalogEntry{
		ID:           definition.ID,
		Key:          definition.Key,
		Label:        definition.Label,
		Category:     definition.Category,
		Type:         definition.Type,
		DisplayValue: telegramSafeSettingValue(value),
		RawValue:     value,
	}
}

func telegramBoolSetting(id, key, label, category string, read func(*SystemSettings) bool, write func(*SystemSettings, bool)) telegramSettingDefinition {
	return telegramSettingDefinition{
		ID: id, Key: key, Label: label, Category: category, Type: TelegramSettingTypeBool,
		Read: func(settings *SystemSettings) string { return strconv.FormatBool(read(settings)) },
		Validate: func(value string) (string, error) {
			if value != "true" && value != "false" {
				return "", infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", "expected true or false")
			}
			return value, nil
		},
		Apply: func(settings *SystemSettings, value string) error {
			write(settings, value == "true")
			return nil
		},
	}
}

func telegramIntSetting(id, key, label, category string, min, max int, read func(*SystemSettings) int, write func(*SystemSettings, int)) telegramSettingDefinition {
	return telegramSettingDefinition{
		ID: id, Key: key, Label: label, Category: category, Type: TelegramSettingTypeInt,
		Read: func(settings *SystemSettings) string { return strconv.Itoa(read(settings)) },
		Validate: func(value string) (string, error) {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < min || parsed > max {
				return "", infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", fmt.Sprintf("expected an integer between %d and %d", min, max))
			}
			return strconv.Itoa(parsed), nil
		},
		Apply: func(settings *SystemSettings, value string) error {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", "expected an integer")
			}
			write(settings, parsed)
			return nil
		},
	}
}

func telegramFloatSetting(id, key, label, category string, min, max float64, read func(*SystemSettings) float64, write func(*SystemSettings, float64)) telegramSettingDefinition {
	return telegramSettingDefinition{
		ID: id, Key: key, Label: label, Category: category, Type: TelegramSettingTypeFloat,
		Read: func(settings *SystemSettings) string { return strconv.FormatFloat(read(settings), 'f', -1, 64) },
		Validate: func(value string) (string, error) {
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < min || parsed > max {
				return "", infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", fmt.Sprintf("expected a finite number between %s and %s", formatTelegramFloat(min), formatTelegramFloat(max)))
			}
			return strconv.FormatFloat(parsed, 'f', -1, 64), nil
		},
		Apply: func(settings *SystemSettings, value string) error {
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", "expected a decimal number")
			}
			write(settings, parsed)
			return nil
		},
	}
}

func telegramTextSetting(id, key, label, category string, maxRunes int, allowEmpty bool, read func(*SystemSettings) string, write func(*SystemSettings, string)) telegramSettingDefinition {
	return telegramSettingDefinition{
		ID: id, Key: key, Label: label, Category: category, Type: TelegramSettingTypeText,
		Read: read,
		Validate: func(value string) (string, error) {
			if !allowEmpty && value == "" {
				return "", infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", "value cannot be empty")
			}
			if len([]rune(value)) > maxRunes || strings.ContainsAny(value, "\x00\r") {
				return "", infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", "text value is invalid or too long")
			}
			return value, nil
		},
		Apply: func(settings *SystemSettings, value string) error {
			write(settings, value)
			return nil
		},
	}
}

func telegramURLSetting(id, key, label, category string, maxRunes int, allowEmpty bool, read func(*SystemSettings) string, write func(*SystemSettings, string)) telegramSettingDefinition {
	definition := telegramTextSetting(id, key, label, category, maxRunes, allowEmpty, read, write)
	definition.Type = TelegramSettingTypeURL
	textValidate := definition.Validate
	definition.Validate = func(value string) (string, error) {
		value, err := textValidate(value)
		if err != nil || value == "" {
			return value, err
		}
		if err := config.ValidateAbsoluteHTTPURL(value); err != nil {
			return "", infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", "expected an absolute HTTP(S) URL")
		}
		return value, nil
	}
	return definition
}

func telegramEnumSetting(id, key, label, category string, allowed []string, read func(*SystemSettings) string, write func(*SystemSettings, string)) telegramSettingDefinition {
	allowedValues := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedValues[value] = struct{}{}
	}
	return telegramSettingDefinition{
		ID: id, Key: key, Label: label, Category: category, Type: TelegramSettingTypeEnum,
		Read: read,
		Validate: func(value string) (string, error) {
			if _, ok := allowedValues[value]; !ok {
				return "", infraerrors.BadRequest("INVALID_TELEGRAM_SETTING_VALUE", fmt.Sprintf("value must be one of %s", strings.Join(allowed, ", ")))
			}
			return value, nil
		},
		Apply: func(settings *SystemSettings, value string) error {
			write(settings, value)
			return nil
		},
	}
}

func validateTelegramSettingValueForDefinition(definition telegramSettingDefinition, value string) (string, error) {
	if len(value) > telegramSettingMaxInputBytes {
		return "", infraerrors.BadRequest("TELEGRAM_SETTING_TOO_LONG", "setting value is too long")
	}
	return definition.Validate(strings.TrimSpace(value))
}

func validateTelegramSettingValue(entry TelegramSettingCatalogEntry, value string) (string, error) {
	definition, ok := telegramSettingDefinitionByID(entry.ID)
	if !ok || definition.Key != entry.Key || definition.Type != entry.Type {
		return "", infraerrors.BadRequest("INVALID_TELEGRAM_SETTING", "setting is not available in Telegram")
	}
	return validateTelegramSettingValueForDefinition(definition, value)
}

func formatTelegramFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func telegramSettingID(key string) string {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(key))
	return strconv.FormatUint(hash.Sum64(), 36)
}

func telegramSafeSettingValue(value string) string {
	if value == "" {
		return "（空）"
	}
	return truncateTelegramText(value, 180)
}
