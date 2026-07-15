CREATE TABLE IF NOT EXISTS notification_channels (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    send_resolved BOOLEAN NOT NULL DEFAULT TRUE,
    repeat_interval_seconds INTEGER NOT NULL,
    event_types JSONB NOT NULL DEFAULT '[]'::jsonb,
    project_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    secret_ciphertext BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_channels_active_name
    ON notification_channels(name) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS alert_incidents (
    id TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL,
    kind TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    project_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('firing', 'resolved')),
    severity TEXT NOT NULL,
    summary TEXT NOT NULL,
    description TEXT NOT NULL,
    source_event_id TEXT NOT NULL,
    occurrence_count INTEGER NOT NULL DEFAULT 1,
    started_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    resolved_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_alert_incidents_firing_fingerprint
    ON alert_incidents(fingerprint) WHERE status = 'firing';
CREATE INDEX IF NOT EXISTS idx_alert_incidents_updated_at
    ON alert_incidents(updated_at DESC);

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id TEXT PRIMARY KEY,
    alert_id TEXT NOT NULL REFERENCES alert_incidents(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES notification_channels(id) ON DELETE RESTRICT,
    transition TEXT NOT NULL CHECK (transition IN ('firing', 'repeat', 'resolved')),
    dedupe_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'delivering', 'sent', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    sent_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_notification_deliveries_claim
    ON notification_deliveries(status, next_attempt_at, created_at);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_created_at
    ON notification_deliveries(created_at DESC);
