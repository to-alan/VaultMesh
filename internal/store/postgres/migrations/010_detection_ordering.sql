-- Keep the originating command's order with the report itself. Commands have
-- a shorter retention window than detection reports, so report ordering must
-- not depend on the command row continuing to exist.
ALTER TABLE detection_reports
    ADD COLUMN IF NOT EXISTS command_created_at TIMESTAMPTZ;

UPDATE detection_reports AS report
SET command_created_at = command.created_at
FROM commands AS command
WHERE command.id = report.command_id
  AND report.command_created_at IS NULL;
