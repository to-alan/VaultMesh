ALTER TABLE servers ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_runs_started_at ON runs (started_at);
CREATE INDEX IF NOT EXISTS idx_commands_created_at ON commands (created_at);
CREATE INDEX IF NOT EXISTS idx_audit_events_created_at ON audit_events (created_at);
CREATE INDEX IF NOT EXISTS idx_alert_incidents_updated_at ON alert_incidents (updated_at);
