-- Server-scoped commands (type = "detect") carry no project reference.
ALTER TABLE commands ALTER COLUMN project_id DROP NOT NULL;

CREATE TABLE IF NOT EXISTS detection_reports (
    server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    command_id TEXT NOT NULL,
    report JSONB NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL
);
