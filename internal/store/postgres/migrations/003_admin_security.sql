CREATE TABLE IF NOT EXISTS admin_account (
    id TEXT PRIMARY KEY CHECK (id = 'admin'),
    username TEXT NOT NULL UNIQUE,
    password_hash BYTEA NOT NULL,
    webauthn_user_id BYTEA NOT NULL UNIQUE,
    security_data BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
