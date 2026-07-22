package repository

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	telegramVerificationAdminKeyPrefix   = "telegram:verification:admin:"
	telegramVerificationCodeKeyPrefix    = "telegram:verification:code:"
	telegramVerificationAttemptKeyPrefix = "telegram:verification:attempt:"
	telegramUpdateKeyPrefix              = "telegram:update:"
	telegramPendingInputKeyPrefix        = "telegram:pending-input:"
	telegramConfigLockKey                = "telegram:config-lock"
	telegramVerificationIssueAttempts    = 16
)

var (
	telegramVerificationCodeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	telegramVerificationCodeRange    = big.NewInt(int64(len(telegramVerificationCodeAlphabet)))

	telegramIssueVerificationCodeScript = redis.NewScript(`
local owner = redis.call("GET", KEYS[2])
if owner and owner ~= ARGV[1] then
  return 0
end
local oldHash = redis.call("GET", KEYS[1])
if oldHash then
  local oldCodeKey = ARGV[3] .. oldHash
  if redis.call("GET", oldCodeKey) == ARGV[1] then
    redis.call("DEL", oldCodeKey)
  end
end
redis.call("SET", KEYS[2], ARGV[1], "PX", ARGV[2])
redis.call("SET", KEYS[1], ARGV[4], "PX", ARGV[2])
return 1
`)

	telegramVerificationStatusScript = redis.NewScript(`
local codeHash = redis.call("GET", KEYS[1])
if not codeHash then
  return -1
end
local codeKey = ARGV[2] .. codeHash
if redis.call("GET", codeKey) ~= ARGV[1] then
  redis.call("DEL", KEYS[1])
  return -1
end
local adminTTL = redis.call("PTTL", KEYS[1])
local codeTTL = redis.call("PTTL", codeKey)
if adminTTL <= 0 or codeTTL <= 0 then
  redis.call("DEL", KEYS[1], codeKey)
  return -1
end
if adminTTL < codeTTL then
  return adminTTL
end
return codeTTL
`)

	telegramCancelVerificationCodeScript = redis.NewScript(`
local codeHash = redis.call("GET", KEYS[1])
if not codeHash then
  return 0
end
local codeKey = ARGV[2] .. codeHash
if redis.call("GET", codeKey) == ARGV[1] then
  redis.call("DEL", codeKey)
end
redis.call("DEL", KEYS[1])
return 1
`)

	telegramConsumeVerificationCodeScript = redis.NewScript(`
local adminID = redis.call("GET", KEYS[1])
if not adminID then
  return {}
end
local adminKey = ARGV[2] .. adminID
if redis.call("GET", adminKey) ~= ARGV[1] then
  return {}
end
local codeTTL = redis.call("PTTL", KEYS[1])
local adminTTL = redis.call("PTTL", adminKey)
if codeTTL <= 0 or adminTTL <= 0 then
  redis.call("DEL", KEYS[1], adminKey)
  return {}
end
local remaining = codeTTL
if adminTTL < remaining then
  remaining = adminTTL
end
redis.call("DEL", KEYS[1], adminKey)
return {adminID, remaining}
`)

	telegramRestoreVerificationCodeScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 1 or redis.call("EXISTS", KEYS[2]) == 1 then
  return 0
end
redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[3])
redis.call("SET", KEYS[2], ARGV[2], "PX", ARGV[3])
return 1
`)

	telegramAllowVerificationAttemptScript = redis.NewScript(`
local attempts = redis.call("INCR", KEYS[1])
local ttl = redis.call("PTTL", KEYS[1])
local window = tonumber(ARGV[1])
if attempts == 1 or ttl < 0 or ttl > window then
  redis.call("PEXPIRE", KEYS[1], window)
end
if attempts > tonumber(ARGV[2]) then
  return 0
end
return 1
`)

	telegramTakePendingInputScript = redis.NewScript(`
local value = redis.call("GET", KEYS[1])
if not value then
  return false
end
redis.call("DEL", KEYS[1])
return value
`)

	telegramTakePendingInputIfNonceScript = redis.NewScript(`
local value = redis.call("GET", KEYS[1])
if not value then
  return false
end
local ok, pending = pcall(cjson.decode, value)
if not ok or pending["operation_nonce"] ~= ARGV[1] then
  return false
end
redis.call("DEL", KEYS[1])
return value
`)

	telegramReleaseConfigLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
  return 0
end
return redis.call("DEL", KEYS[1])
`)
)

type telegramStateRepository struct {
	rdb    *redis.Client
	random io.Reader
	now    func() time.Time
}

func NewTelegramStateRepository(rdb *redis.Client) service.TelegramStateRepository {
	return &telegramStateRepository{
		rdb:    rdb,
		random: cryptorand.Reader,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (r *telegramStateRepository) IssueVerificationCode(ctx context.Context, adminUserID int64) (*service.TelegramVerificationCode, error) {
	if adminUserID <= 0 {
		return nil, service.ErrTelegramVerificationCodeInvalid
	}
	adminID := strconv.FormatInt(adminUserID, 10)
	adminKey := telegramVerificationAdminKeyPrefix + adminID
	ttlMilliseconds := service.TelegramVerificationCodeTTL.Milliseconds()

	for range telegramVerificationIssueAttempts {
		code, err := r.generateVerificationCode()
		if err != nil {
			return nil, fmt.Errorf("generate Telegram verification code: %w", err)
		}
		codeHash := hashTelegramVerificationCode(code)
		codeKey := telegramVerificationCodeKeyPrefix + codeHash
		issued, err := telegramIssueVerificationCodeScript.Run(
			ctx,
			r.rdb,
			[]string{adminKey, codeKey},
			adminID,
			ttlMilliseconds,
			telegramVerificationCodeKeyPrefix,
			codeHash,
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("store Telegram verification code: %w", err)
		}
		if issued == 1 {
			return &service.TelegramVerificationCode{
				Code:      code,
				ExpiresAt: r.now().Add(service.TelegramVerificationCodeTTL),
			}, nil
		}
	}
	return nil, errors.New("could not allocate a unique Telegram verification code")
}

func (r *telegramStateRepository) generateVerificationCode() (string, error) {
	code := make([]byte, service.TelegramVerificationCodeLength)
	for i := range code {
		value, err := cryptorand.Int(r.random, telegramVerificationCodeRange)
		if err != nil {
			return "", err
		}
		code[i] = telegramVerificationCodeAlphabet[value.Int64()]
	}
	return string(code), nil
}

func hashTelegramVerificationCode(code string) string {
	code = normalizeTelegramVerificationCode(code)
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func (r *telegramStateRepository) GetVerificationCodeStatus(ctx context.Context, adminUserID int64) (*service.TelegramVerificationCodeStatus, error) {
	if adminUserID <= 0 {
		return nil, nil
	}
	adminID := strconv.FormatInt(adminUserID, 10)
	ttlMilliseconds, err := telegramVerificationStatusScript.Run(
		ctx,
		r.rdb,
		[]string{telegramVerificationAdminKeyPrefix + adminID},
		adminID,
		telegramVerificationCodeKeyPrefix,
	).Int64()
	if err != nil {
		return nil, fmt.Errorf("get Telegram verification code status: %w", err)
	}
	if ttlMilliseconds <= 0 {
		return nil, nil
	}
	return &service.TelegramVerificationCodeStatus{
		ExpiresAt: r.now().Add(time.Duration(ttlMilliseconds) * time.Millisecond),
	}, nil
}

func (r *telegramStateRepository) CancelVerificationCode(ctx context.Context, adminUserID int64) (bool, error) {
	if adminUserID <= 0 {
		return false, nil
	}
	adminID := strconv.FormatInt(adminUserID, 10)
	removed, err := telegramCancelVerificationCodeScript.Run(
		ctx,
		r.rdb,
		[]string{telegramVerificationAdminKeyPrefix + adminID},
		adminID,
		telegramVerificationCodeKeyPrefix,
	).Int64()
	if err != nil {
		return false, fmt.Errorf("cancel Telegram verification code: %w", err)
	}
	return removed == 1, nil
}

func (r *telegramStateRepository) ConsumeVerificationCode(ctx context.Context, code string) (int64, time.Duration, error) {
	code = normalizeTelegramVerificationCode(code)
	if !isTelegramVerificationCode(code) {
		return 0, 0, service.ErrTelegramVerificationCodeInvalid
	}
	codeHash := hashTelegramVerificationCode(code)
	result, err := telegramConsumeVerificationCodeScript.Run(
		ctx,
		r.rdb,
		[]string{telegramVerificationCodeKeyPrefix + codeHash},
		codeHash,
		telegramVerificationAdminKeyPrefix,
	).Slice()
	if err != nil && err != redis.Nil {
		return 0, 0, fmt.Errorf("consume Telegram verification code: %w", err)
	}
	if len(result) != 2 {
		return 0, 0, service.ErrTelegramVerificationCodeInvalid
	}
	adminID := fmt.Sprint(result[0])
	ttlMilliseconds, err := strconv.ParseInt(fmt.Sprint(result[1]), 10, 64)
	if err != nil || ttlMilliseconds <= 0 {
		return 0, 0, service.ErrTelegramVerificationCodeInvalid
	}
	value, err := strconv.ParseInt(adminID, 10, 64)
	if err != nil || value <= 0 {
		return 0, 0, service.ErrTelegramVerificationCodeInvalid
	}
	return value, time.Duration(ttlMilliseconds) * time.Millisecond, nil
}

func (r *telegramStateRepository) RestoreVerificationCode(ctx context.Context, code string, adminUserID int64, ttl time.Duration) (bool, error) {
	code = normalizeTelegramVerificationCode(code)
	if !isTelegramVerificationCode(code) || adminUserID <= 0 || ttl <= 0 {
		return false, service.ErrTelegramVerificationCodeInvalid
	}
	codeHash := hashTelegramVerificationCode(code)
	adminID := strconv.FormatInt(adminUserID, 10)
	restored, err := telegramRestoreVerificationCodeScript.Run(
		ctx,
		r.rdb,
		[]string{
			telegramVerificationCodeKeyPrefix + codeHash,
			telegramVerificationAdminKeyPrefix + adminID,
		},
		adminID,
		codeHash,
		ttl.Milliseconds(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("restore Telegram verification code: %w", err)
	}
	return restored == 1, nil
}

func (r *telegramStateRepository) AllowVerificationAttempt(ctx context.Context, telegramUserID int64) (bool, error) {
	if telegramUserID <= 0 {
		return false, service.ErrTelegramIdentityInvalid
	}
	allowed, err := telegramAllowVerificationAttemptScript.Run(
		ctx,
		r.rdb,
		[]string{telegramVerificationAttemptKeyPrefix + strconv.FormatInt(telegramUserID, 10)},
		service.TelegramVerificationAttemptTTL.Milliseconds(),
		service.TelegramVerificationAttemptLimit,
	).Int64()
	if err != nil {
		return false, fmt.Errorf("limit Telegram verification attempts: %w", err)
	}
	return allowed == 1, nil
}

func (r *telegramStateRepository) ClearVerificationAttempts(ctx context.Context, telegramUserID int64) error {
	if telegramUserID <= 0 {
		return nil
	}
	if err := r.rdb.Del(ctx, telegramVerificationAttemptKeyPrefix+strconv.FormatInt(telegramUserID, 10)).Err(); err != nil {
		return fmt.Errorf("clear Telegram verification attempts: %w", err)
	}
	return nil
}

func isTelegramVerificationCode(code string) bool {
	code = normalizeTelegramVerificationCode(code)
	if len(code) != service.TelegramVerificationCodeLength {
		return false
	}
	for i := range code {
		if code[i] < '0' || code[i] > '9' && code[i] < 'A' || code[i] > 'Z' {
			return false
		}
	}
	return true
}

func normalizeTelegramVerificationCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, " ", "")
	code = strings.ReplaceAll(code, "-", "")
	return code
}

func (r *telegramStateRepository) ClaimUpdate(ctx context.Context, updateID int64) (bool, error) {
	claimed, err := r.rdb.SetNX(
		ctx,
		telegramUpdateKeyPrefix+strconv.FormatInt(updateID, 10),
		"1",
		service.TelegramUpdateProcessingTTL,
	).Result()
	if err != nil {
		return false, fmt.Errorf("claim Telegram update: %w", err)
	}
	return claimed, nil
}

func (r *telegramStateRepository) CompleteUpdate(ctx context.Context, updateID int64) error {
	if err := r.rdb.Set(
		ctx,
		telegramUpdateKeyPrefix+strconv.FormatInt(updateID, 10),
		"done",
		service.TelegramUpdateDeduplicationTTL,
	).Err(); err != nil {
		return fmt.Errorf("complete Telegram update: %w", err)
	}
	return nil
}

func (r *telegramStateRepository) ReleaseUpdate(ctx context.Context, updateID int64) error {
	if err := r.rdb.Del(ctx, telegramUpdateKeyPrefix+strconv.FormatInt(updateID, 10)).Err(); err != nil {
		return fmt.Errorf("release Telegram update: %w", err)
	}
	return nil
}

func (r *telegramStateRepository) AcquireConfigLock(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || ttl <= 0 {
		return false, service.ErrTelegramIdentityInvalid
	}
	acquired, err := r.rdb.SetNX(ctx, telegramConfigLockKey, owner, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("acquire Telegram configuration lock: %w", err)
	}
	return acquired, nil
}

func (r *telegramStateRepository) ReleaseConfigLock(ctx context.Context, owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil
	}
	if _, err := telegramReleaseConfigLockScript.Run(ctx, r.rdb, []string{telegramConfigLockKey}, owner).Int64(); err != nil {
		return fmt.Errorf("release Telegram configuration lock: %w", err)
	}
	return nil
}

func (r *telegramStateRepository) SetPendingSettingInput(ctx context.Context, telegramUserID int64, input service.TelegramPendingSettingInput) error {
	input.SettingKey = strings.TrimSpace(input.SettingKey)
	input.InputType = service.TelegramSettingInputType(strings.TrimSpace(string(input.InputType)))
	input.ExpiresAt = input.ExpiresAt.UTC()
	ttl := input.ExpiresAt.Sub(r.now())
	if telegramUserID <= 0 || input.SettingKey == "" || input.InputType == "" || ttl <= 0 {
		return service.ErrTelegramPendingInputInvalid
	}
	value, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal Telegram pending setting input: %w", err)
	}
	if err := r.rdb.Set(ctx, telegramPendingInputKey(telegramUserID), value, ttl).Err(); err != nil {
		return fmt.Errorf("store Telegram pending setting input: %w", err)
	}
	return nil
}

func (r *telegramStateRepository) GetPendingSettingInput(ctx context.Context, telegramUserID int64) (*service.TelegramPendingSettingInput, error) {
	value, err := r.rdb.Get(ctx, telegramPendingInputKey(telegramUserID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get Telegram pending setting input: %w", err)
	}
	return decodeTelegramPendingSettingInput(value)
}

func (r *telegramStateRepository) TakePendingSettingInput(ctx context.Context, telegramUserID int64) (*service.TelegramPendingSettingInput, error) {
	value, err := telegramTakePendingInputScript.Run(
		ctx,
		r.rdb,
		[]string{telegramPendingInputKey(telegramUserID)},
	).Text()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("take Telegram pending setting input: %w", err)
	}
	return decodeTelegramPendingSettingInput([]byte(value))
}

func (r *telegramStateRepository) TakePendingSettingInputIfNonce(ctx context.Context, telegramUserID int64, nonce string) (*service.TelegramPendingSettingInput, error) {
	nonce = strings.TrimSpace(nonce)
	if telegramUserID <= 0 || nonce == "" {
		return nil, service.ErrTelegramPendingInputInvalid
	}
	value, err := telegramTakePendingInputIfNonceScript.Run(
		ctx,
		r.rdb,
		[]string{telegramPendingInputKey(telegramUserID)},
		nonce,
	).Text()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("take Telegram pending setting input by nonce: %w", err)
	}
	return decodeTelegramPendingSettingInput([]byte(value))
}

func (r *telegramStateRepository) DeletePendingSettingInput(ctx context.Context, telegramUserID int64) (bool, error) {
	deleted, err := r.rdb.Del(ctx, telegramPendingInputKey(telegramUserID)).Result()
	if err != nil {
		return false, fmt.Errorf("delete Telegram pending setting input: %w", err)
	}
	return deleted == 1, nil
}

func telegramPendingInputKey(telegramUserID int64) string {
	return telegramPendingInputKeyPrefix + strconv.FormatInt(telegramUserID, 10)
}

func decodeTelegramPendingSettingInput(value []byte) (*service.TelegramPendingSettingInput, error) {
	var input service.TelegramPendingSettingInput
	if err := json.Unmarshal(value, &input); err != nil {
		return nil, fmt.Errorf("unmarshal Telegram pending setting input: %w", err)
	}
	return &input, nil
}
