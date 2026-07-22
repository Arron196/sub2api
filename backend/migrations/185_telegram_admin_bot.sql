-- Durable application-admin bindings for the Telegram admin bot.
CREATE TABLE IF NOT EXISTS telegram_admin_bindings (
    id BIGSERIAL PRIMARY KEY,
    admin_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    telegram_user_id BIGINT NOT NULL CHECK (telegram_user_id > 0),
    private_chat_id BIGINT NOT NULL CHECK (private_chat_id > 0),
    telegram_username VARCHAR(64) NOT NULL DEFAULT '',
    display_name VARCHAR(160) NOT NULL DEFAULT '',
    bound_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    UNIQUE (admin_user_id, telegram_user_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_telegram_admin_bindings_active_admin
    ON telegram_admin_bindings (admin_user_id)
    WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_telegram_admin_bindings_active_telegram
    ON telegram_admin_bindings (telegram_user_id)
    WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_telegram_admin_bindings_active_chat
    ON telegram_admin_bindings (private_chat_id)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_telegram_admin_bindings_admin_history
    ON telegram_admin_bindings (admin_user_id, bound_at DESC);

COMMENT ON TABLE telegram_admin_bindings IS
    'Application admin to Telegram private-chat bindings with revocation history';
