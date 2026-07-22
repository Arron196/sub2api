package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestTelegramAdminRepositoryBindRevokesConflictsAndUpsertsAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewTelegramAdminRepository(db)

	identity := service.TelegramIdentity{
		UserID: 88001, PrivateChatID: 88001, Username: "admin_user", DisplayName: "Admin User",
	}
	boundAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	_, err = repo.Bind(context.Background(), 7, service.TelegramIdentity{UserID: 88001, PrivateChatID: 99001})
	require.ErrorIs(t, err, service.ErrTelegramIdentityInvalid)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock.*telegram-admin`).
		WithArgs(int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock.*telegram-user`).
		WithArgs(identity.UserID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE telegram_admin_bindings.*revoked_at`).
		WithArgs(int64(7), identity.UserID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(`(?s)INSERT INTO telegram_admin_bindings.*ON CONFLICT.*RETURNING`).
		WithArgs(int64(7), identity.UserID, identity.PrivateChatID, identity.Username, identity.DisplayName, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "admin_user_id", "telegram_user_id", "private_chat_id", "telegram_username",
			"display_name", "bound_at", "updated_at", "revoked_at",
		}).AddRow(int64(9), int64(7), identity.UserID, identity.PrivateChatID, identity.Username, identity.DisplayName, boundAt, boundAt, nil))
	mock.ExpectCommit()

	binding, err := repo.Bind(context.Background(), 7, identity)
	require.NoError(t, err)
	require.Equal(t, int64(9), binding.ID)
	require.Equal(t, int64(7), binding.AdminUserID)
	require.Equal(t, identity.UserID, binding.TelegramUserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTelegramAdminMigrationContainsOnlyDurableBindings(t *testing.T) {
	content, err := migrations.FS.ReadFile("185_telegram_admin_bot.sql")
	require.NoError(t, err)
	sqlText := string(content)
	for _, required := range []string{
		"REFERENCES users(id)",
		"telegram_user_id BIGINT",
		"private_chat_id BIGINT",
		"uq_telegram_admin_bindings_active_admin",
		"uq_telegram_admin_bindings_active_telegram",
	} {
		require.Contains(t, sqlText, required)
	}
	for _, temporaryTable := range []string{
		"telegram_binding_challenges",
		"telegram_processed_updates",
		"token_hash",
	} {
		require.NotContains(t, sqlText, temporaryTable)
	}
}

func TestGroupRateAuthCacheInvalidationMigrationCoversAllMultipliers(t *testing.T) {
	content, err := migrations.FS.ReadFile("186_group_rate_auth_cache_invalidation.sql")
	require.NoError(t, err)
	sqlText := string(content)
	for _, field := range []string{
		"rate_multiplier",
		"image_rate_multiplier",
		"video_rate_multiplier",
		"peak_rate_multiplier",
	} {
		require.Contains(t, sqlText, "OLD."+field+" IS NOT DISTINCT FROM NEW."+field)
	}
	require.Contains(t, sqlText, "INSERT INTO auth_cache_invalidation_outbox")
	require.Contains(t, sqlText, "WHERE k.group_id = target_group_id")
}

func TestTelegramGroupRateMutationLocksAuthorizationAndUpdatesOnlySelectedColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewTelegramGroupRateMutationRepository(db)
	ctx := context.Background()
	identity := service.TelegramIdentity{UserID: 88001, PrivateChatID: 88001}
	boundAt := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	groupUpdatedAt := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	mutationUpdatedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM telegram_admin_bindings.*FOR UPDATE OF b, u`).
		WithArgs(int64(9), identity.UserID, identity.PrivateChatID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "admin_user_id", "email", "telegram_user_id", "private_chat_id",
			"telegram_username", "display_name", "bound_at", "updated_at", "revoked_at", "role", "status",
		}).AddRow(int64(9), int64(7), "admin@example.com", identity.UserID, identity.PrivateChatID, "admin", "Admin", boundAt, boundAt, nil, service.RoleAdmin, service.StatusActive))
	mock.ExpectQuery(`(?s)FROM groups.*WHERE id = \$1.*FOR UPDATE`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "platform", "status", "subscription_type", "rate_multiplier",
			"image_rate_independent", "image_rate_multiplier", "video_rate_independent",
			"video_rate_multiplier", "peak_rate_enabled", "peak_rate_multiplier", "updated_at",
		}).AddRow(int64(42), "Primary", service.PlatformOpenAI, service.StatusActive, service.SubscriptionTypeStandard,
			1.0, true, 1.25, false, 1.0, false, 1.0, groupUpdatedAt))
	mock.ExpectQuery(`^UPDATE groups SET rate_multiplier = \$1, updated_at = NOW\(\) WHERE id = \$2 RETURNING updated_at$`).
		WithArgs(1.75, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(mutationUpdatedAt))
	mock.ExpectExec(`(?s)INSERT INTO scheduler_outbox`).
		WithArgs(service.SchedulerOutboxEventGroupChanged, nil, sqlmock.AnyArg(), nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	mutation, err := repo.UpdateAuthorizedGroupRateMultiplier(
		ctx, 9, identity, 42, service.TelegramGroupRateKindBase, 1.75, groupUpdatedAt,
	)
	require.NoError(t, err)
	require.Equal(t, 1.0, mutation.Before)
	require.Equal(t, 1.75, mutation.After)
	require.Equal(t, 1.75, mutation.Group.RateMultiplier)
	require.Equal(t, 1.25, mutation.Group.ImageRateMultiplier)
	require.Equal(t, int64(7), mutation.Admin.ID)
	require.Equal(t, int64(9), mutation.Binding.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTelegramGroupRateMutationRejectsChangedGroupBeforeWrite(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewTelegramGroupRateMutationRepository(db)
	identity := service.TelegramIdentity{UserID: 88001, PrivateChatID: 88001}
	expected := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	current := expected.Add(time.Minute)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM telegram_admin_bindings.*FOR UPDATE OF b, u`).
		WithArgs(int64(9), identity.UserID, identity.PrivateChatID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "admin_user_id", "email", "telegram_user_id", "private_chat_id",
			"telegram_username", "display_name", "bound_at", "updated_at", "revoked_at", "role", "status",
		}).AddRow(int64(9), int64(7), "admin@example.com", identity.UserID, identity.PrivateChatID, "admin", "Admin", expected, expected, nil, service.RoleAdmin, service.StatusActive))
	mock.ExpectQuery(`(?s)FROM groups.*WHERE id = \$1.*FOR UPDATE`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "platform", "status", "subscription_type", "rate_multiplier",
			"image_rate_independent", "image_rate_multiplier", "video_rate_independent",
			"video_rate_multiplier", "peak_rate_enabled", "peak_rate_multiplier", "updated_at",
		}).AddRow(int64(42), "Primary", service.PlatformOpenAI, service.StatusActive, service.SubscriptionTypeStandard,
			1.0, true, 1.25, false, 1.0, false, 1.0, current))
	mock.ExpectRollback()

	mutation, err := repo.UpdateAuthorizedGroupRateMultiplier(
		context.Background(), 9, identity, 42, service.TelegramGroupRateKindBase, 1.75, expected,
	)
	require.Nil(t, mutation)
	require.ErrorIs(t, err, service.ErrTelegramGroupChanged)
	require.NoError(t, mock.ExpectationsWereMet())
}
