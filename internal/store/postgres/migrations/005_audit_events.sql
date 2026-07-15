CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL CHECK (outcome IN ('succeeded', 'failed')),
    client_ip TEXT NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL CHECK (status_code BETWEEN 100 AND 599),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_events_created_at
    ON audit_events(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_events_action_created_at
    ON audit_events(action, created_at DESC);
