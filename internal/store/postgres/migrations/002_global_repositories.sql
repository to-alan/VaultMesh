ALTER TABLE repositories DROP CONSTRAINT IF EXISTS repositories_server_id_name_key;
ALTER TABLE repositories DROP CONSTRAINT IF EXISTS repositories_server_id_fkey;
ALTER TABLE repositories DROP COLUMN IF EXISTS server_id;
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 's3_compatible';

DROP INDEX IF EXISTS idx_repositories_name_unique;
