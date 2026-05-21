CREATE TABLE IF NOT EXISTS debug_export_jobs (
    id BIGSERIAL PRIMARY KEY,
    status VARCHAR(32) NOT NULL,
    options JSONB NOT NULL,
    created_by BIGINT NOT NULL,
    progress_percent INTEGER NOT NULL DEFAULT 0,
    phase VARCHAR(128) NOT NULL DEFAULT 'queued',
    bytes_written BIGINT NOT NULL DEFAULT 0,
    file_name TEXT,
    artifact_path TEXT,
    file_size BIGINT,
    sha256 VARCHAR(64),
    error_message TEXT,
    canceled_by BIGINT,
    canceled_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_debug_export_jobs_status_created_at ON debug_export_jobs(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_debug_export_jobs_created_by_created_at ON debug_export_jobs(created_by, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_debug_export_jobs_expires_at ON debug_export_jobs(expires_at);
CREATE INDEX IF NOT EXISTS idx_debug_export_jobs_status_last_heartbeat_at ON debug_export_jobs(status, last_heartbeat_at);
