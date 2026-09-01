ALTER TABLE notification_channels
    ADD COLUMN IF NOT EXISTS server_ids JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE alert_incidents
    ALTER COLUMN project_id DROP NOT NULL;

ALTER TABLE alert_incidents
    ALTER COLUMN project_name DROP NOT NULL;

ALTER TABLE alert_incidents
    ADD COLUMN IF NOT EXISTS resource_type TEXT NOT NULL DEFAULT 'project';

ALTER TABLE alert_incidents
    ADD COLUMN IF NOT EXISTS resource_id TEXT NOT NULL DEFAULT '';

ALTER TABLE alert_incidents
    ADD COLUMN IF NOT EXISTS resource_name TEXT NOT NULL DEFAULT '';

UPDATE alert_incidents
SET resource_type = 'project',
    resource_id = COALESCE(project_id, ''),
    resource_name = COALESCE(project_name, '')
WHERE resource_id = '' AND project_id IS NOT NULL;
