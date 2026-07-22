package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type telegramAdminRepository struct {
	db *sql.DB
}

func NewTelegramAdminRepository(db *sql.DB) service.TelegramAdminBindingRepository {
	return &telegramAdminRepository{db: db}
}

func NewTelegramGroupRateMutationRepository(db *sql.DB) service.TelegramGroupRateMutationRepository {
	return &telegramAdminRepository{db: db}
}

func (r *telegramAdminRepository) Bind(ctx context.Context, adminUserID int64, identity service.TelegramIdentity) (*service.TelegramBinding, error) {
	if adminUserID <= 0 || identity.UserID <= 0 || identity.PrivateChatID != identity.UserID {
		return nil, service.ErrTelegramIdentityInvalid
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin Telegram binding transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('telegram-admin:' || $1::text, 0))`, adminUserID); err != nil {
		return nil, fmt.Errorf("lock Telegram admin binding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('telegram-user:' || $1::text, 0))`, identity.UserID); err != nil {
		return nil, fmt.Errorf("lock Telegram user binding: %w", err)
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE telegram_admin_bindings
		SET revoked_at = $3, updated_at = $3
		WHERE revoked_at IS NULL
		  AND (admin_user_id = $1 OR telegram_user_id = $2)`,
		adminUserID, identity.UserID, now); err != nil {
		return nil, fmt.Errorf("revoke conflicting Telegram bindings: %w", err)
	}

	binding := &service.TelegramBinding{}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO telegram_admin_bindings (
			admin_user_id, telegram_user_id, private_chat_id,
			telegram_username, display_name, bound_at, updated_at, revoked_at
		) VALUES ($1, $2, $3, $4, $5, $6, $6, NULL)
		ON CONFLICT (admin_user_id, telegram_user_id) DO UPDATE SET
			private_chat_id = EXCLUDED.private_chat_id,
			telegram_username = EXCLUDED.telegram_username,
			display_name = EXCLUDED.display_name,
			bound_at = EXCLUDED.bound_at,
			updated_at = EXCLUDED.updated_at,
			revoked_at = NULL
		RETURNING id, admin_user_id, telegram_user_id, private_chat_id,
		          telegram_username, display_name, bound_at, updated_at, revoked_at`,
		adminUserID, identity.UserID, identity.PrivateChatID,
		identity.Username, identity.DisplayName, now,
	).Scan(
		&binding.ID, &binding.AdminUserID, &binding.TelegramUserID, &binding.PrivateChatID,
		&binding.TelegramUsername, &binding.DisplayName, &binding.BoundAt, &binding.UpdatedAt, &binding.RevokedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert Telegram admin binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Telegram binding transaction: %w", err)
	}
	return binding, nil
}

const telegramBindingSelectColumns = `
	b.id, b.admin_user_id, u.email, b.telegram_user_id, b.private_chat_id,
	b.telegram_username, b.display_name, b.bound_at, b.updated_at, b.revoked_at`

func scanTelegramBinding(row interface{ Scan(...any) error }) (*service.TelegramBinding, error) {
	binding := &service.TelegramBinding{}
	err := row.Scan(
		&binding.ID, &binding.AdminUserID, &binding.AdminEmail,
		&binding.TelegramUserID, &binding.PrivateChatID, &binding.TelegramUsername,
		&binding.DisplayName, &binding.BoundAt, &binding.UpdatedAt, &binding.RevokedAt,
	)
	return binding, err
}

func (r *telegramAdminRepository) ListActiveBindings(ctx context.Context, adminUserID int64) ([]*service.TelegramBinding, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+telegramBindingSelectColumns+`
		FROM telegram_admin_bindings b
		JOIN users u ON u.id = b.admin_user_id
		WHERE b.admin_user_id = $1 AND b.revoked_at IS NULL
		ORDER BY b.bound_at DESC`, adminUserID)
	if err != nil {
		return nil, fmt.Errorf("list Telegram bindings: %w", err)
	}
	defer rows.Close()

	bindings := make([]*service.TelegramBinding, 0, 1)
	for rows.Next() {
		binding, err := scanTelegramBinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Telegram binding: %w", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Telegram bindings: %w", err)
	}
	return bindings, nil
}

func (r *telegramAdminRepository) GetActiveBindingByTelegramUserID(ctx context.Context, telegramUserID int64) (*service.TelegramBinding, error) {
	binding, err := scanTelegramBinding(r.db.QueryRowContext(ctx, `SELECT `+telegramBindingSelectColumns+`
		FROM telegram_admin_bindings b
		JOIN users u ON u.id = b.admin_user_id
		WHERE b.telegram_user_id = $1 AND b.revoked_at IS NULL`, telegramUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrTelegramBindingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active Telegram binding: %w", err)
	}
	return binding, nil
}

func (r *telegramAdminRepository) RevokeBinding(ctx context.Context, bindingID, adminUserID int64) (*service.TelegramBinding, error) {
	binding, err := scanTelegramBinding(r.db.QueryRowContext(ctx, `
		WITH revoked AS (
			UPDATE telegram_admin_bindings
			SET revoked_at = NOW(), updated_at = NOW()
			WHERE id = $1 AND admin_user_id = $2 AND revoked_at IS NULL
			RETURNING *
		)
		SELECT r.id, r.admin_user_id, u.email, r.telegram_user_id, r.private_chat_id,
		       r.telegram_username, r.display_name, r.bound_at, r.updated_at, r.revoked_at
		FROM revoked r
		JOIN users u ON u.id = r.admin_user_id`, bindingID, adminUserID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrTelegramBindingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("revoke Telegram binding: %w", err)
	}
	return binding, nil
}

func (r *telegramAdminRepository) UpdateAuthorizedGroupRateMultiplier(
	ctx context.Context,
	bindingID int64,
	identity service.TelegramIdentity,
	groupID int64,
	kind string,
	multiplier float64,
	expectedUpdatedAt time.Time,
) (*service.TelegramGroupRateMutation, error) {
	if bindingID <= 0 || identity.UserID <= 0 || identity.PrivateChatID != identity.UserID || groupID <= 0 ||
		expectedUpdatedAt.IsZero() || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier > 1_000_000 {
		return nil, service.ErrTelegramPendingInputInvalid
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin Telegram group rate transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	binding := &service.TelegramBinding{}
	admin := &service.User{}
	err = tx.QueryRowContext(ctx, `SELECT
		b.id, b.admin_user_id, u.email, b.telegram_user_id, b.private_chat_id,
		b.telegram_username, b.display_name, b.bound_at, b.updated_at, b.revoked_at,
		u.role, u.status
		FROM telegram_admin_bindings b
		JOIN users u ON u.id = b.admin_user_id
		WHERE b.id = $1
		  AND b.telegram_user_id = $2
		  AND b.private_chat_id = $3
		  AND b.revoked_at IS NULL
		  AND u.deleted_at IS NULL
		FOR UPDATE OF b, u`, bindingID, identity.UserID, identity.PrivateChatID).Scan(
		&binding.ID, &binding.AdminUserID, &binding.AdminEmail,
		&binding.TelegramUserID, &binding.PrivateChatID, &binding.TelegramUsername,
		&binding.DisplayName, &binding.BoundAt, &binding.UpdatedAt, &binding.RevokedAt,
		&admin.Role, &admin.Status,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrTelegramBindingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock Telegram binding for group rate update: %w", err)
	}
	admin.ID = binding.AdminUserID
	admin.Email = binding.AdminEmail
	if !admin.IsActive() || !admin.IsAdmin() {
		return nil, service.ErrTelegramBindingNotFound
	}

	group := &service.Group{}
	err = tx.QueryRowContext(ctx, `SELECT
		id, name, platform, status, subscription_type,
		rate_multiplier, image_rate_independent, image_rate_multiplier,
		video_rate_independent, video_rate_multiplier,
		peak_rate_enabled, peak_rate_multiplier, updated_at
		FROM groups
		WHERE id = $1
		FOR UPDATE`, groupID).Scan(
		&group.ID, &group.Name, &group.Platform, &group.Status, &group.SubscriptionType,
		&group.RateMultiplier, &group.ImageRateIndependent, &group.ImageRateMultiplier,
		&group.VideoRateIndependent, &group.VideoRateMultiplier,
		&group.PeakRateEnabled, &group.PeakRateMultiplier, &group.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGroupNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock group for Telegram rate update: %w", err)
	}
	if !group.UpdatedAt.Equal(expectedUpdatedAt) {
		return nil, service.ErrTelegramGroupChanged
	}

	column, before, err := telegramGroupRateColumn(group, kind, multiplier)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("UPDATE groups SET %s = $1, updated_at = NOW() WHERE id = $2 RETURNING updated_at", column)
	if err := tx.QueryRowContext(ctx, query, multiplier, groupID).Scan(&group.UpdatedAt); err != nil {
		return nil, fmt.Errorf("update Telegram group rate multiplier: %w", err)
	}
	setTelegramGroupRateValue(group, kind, multiplier)
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventGroupChanged, nil, &groupID, nil); err != nil {
		return nil, fmt.Errorf("enqueue Telegram group rate update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Telegram group rate transaction: %w", err)
	}
	return &service.TelegramGroupRateMutation{
		Binding: binding,
		Admin:   admin,
		Group:   group,
		Kind:    kind,
		Before:  before,
		After:   multiplier,
	}, nil
}

func telegramGroupRateColumn(group *service.Group, kind string, multiplier float64) (string, float64, error) {
	switch kind {
	case service.TelegramGroupRateKindBase:
		if multiplier <= 0 {
			return "", 0, service.ErrTelegramPendingInputInvalid
		}
		return "rate_multiplier", group.RateMultiplier, nil
	case service.TelegramGroupRateKindImage:
		if !telegramImageRateSupported(group.Platform) || !group.ImageRateIndependent || multiplier < 0 {
			return "", 0, service.ErrTelegramPendingInputInvalid
		}
		return "image_rate_multiplier", group.ImageRateMultiplier, nil
	case service.TelegramGroupRateKindVideo:
		if group.Platform != service.PlatformGrok || !group.VideoRateIndependent || multiplier < 0 {
			return "", 0, service.ErrTelegramPendingInputInvalid
		}
		return "video_rate_multiplier", group.VideoRateMultiplier, nil
	case service.TelegramGroupRateKindPeak:
		if !group.IsSubscriptionType() || !group.PeakRateEnabled || multiplier < 0 {
			return "", 0, service.ErrTelegramPendingInputInvalid
		}
		return "peak_rate_multiplier", group.PeakRateMultiplier, nil
	default:
		return "", 0, service.ErrTelegramPendingInputInvalid
	}
}

func telegramImageRateSupported(platform string) bool {
	switch platform {
	case service.PlatformOpenAI, service.PlatformGemini, service.PlatformAntigravity, service.PlatformGrok:
		return true
	default:
		return false
	}
}

func setTelegramGroupRateValue(group *service.Group, kind string, multiplier float64) {
	switch kind {
	case service.TelegramGroupRateKindBase:
		group.RateMultiplier = multiplier
	case service.TelegramGroupRateKindImage:
		group.ImageRateMultiplier = multiplier
	case service.TelegramGroupRateKindVideo:
		group.VideoRateMultiplier = multiplier
	case service.TelegramGroupRateKindPeak:
		group.PeakRateMultiplier = multiplier
	}
}
