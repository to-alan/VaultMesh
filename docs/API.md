# VaultMesh API quick reference

The API is versioned under `/api/v1`. Administrator access uses a username/password login and a server-side session. The Web application never receives an administrator bearer token.

Log in and store the HttpOnly session cookie:

```bash
curl --fail --silent \
  --cookie-jar vaultmesh-cookie.txt \
  --header 'Content-Type: application/json' \
  --data '{"username":"admin","password":"YOUR_PASSWORD"}' \
  https://api.example.com/api/v1/auth/login
```

Use that cookie for administrator requests:

```bash
curl --fail --silent \
  --cookie vaultmesh-cookie.txt \
  https://api.example.com/api/v1/servers
```

`POST /api/v1/auth/logout` revokes the current session. Session cookies are HttpOnly, SameSite=Lax, non-persistent, and marked Secure when `VAULTMESH_COOKIE_SECURE=true`. Agent endpoints still use the device credential returned by one-time enrollment; this is machine identity and is deliberately separate from administrator login. Secrets and login passwords are accepted only over HTTPS in production.

When TOTP is enabled, password login returns HTTP `202` with `{"mfa_required":true}` and an opaque five-minute challenge cookie. Complete it with `POST /api/v1/auth/totp` and `{"code":"123456"}`; a one-time recovery code is accepted in the same field. A pending challenge is rejected after five failed attempts. Passkey login uses the two-step `POST /api/v1/auth/passkey/begin` and `/finish` WebAuthn ceremony.

Authenticated personal-security endpoints are grouped under `/api/v1/profile`: `GET /profile`, `POST /profile/password`, `/profile/reauthenticate`, `/profile/totp/begin`, `/profile/totp/enable`, `/profile/totp/disable`, `/profile/recovery-codes`, and the `/profile/passkeys/...` registration/deletion ceremonies. TOTP secrets, recovery-code hashes, and WebAuthn credential records are encrypted at rest with `VAULTMESH_MASTER_KEY`. Recovery codes are returned only when created or regenerated.

Adding or deleting a passkey requires an administrator session authenticated within the previous 10 minutes. A stale session receives HTTP `428` with code `reauthentication_required`; call `POST /api/v1/profile/reauthenticate` with `{"password":"...","code":"..."}` and retry. `code` is required only when TOTP is enabled and accepts either a current authenticator code or an unused recovery code. This keeps password and TOTP fields out of the normal passkey workflow while retaining explicit verification for older sessions.

The API service does not serve the Web application. Configure the independently deployed, same-site Web origin in `VAULTMESH_ALLOWED_ORIGINS` and set the Web container's `VAULTMESH_API_BASE_URL` to the browser-visible API URL. Origins are matched exactly, credentialed CORS is enabled only for those origins, and wildcard CORS is intentionally unsupported.

Passkeys additionally use `VAULTMESH_WEBAUTHN_RP_ID`, `VAULTMESH_WEBAUTHN_RP_ORIGINS`, and `VAULTMESH_WEBAUTHN_RP_NAME`. If omitted, RP ID/origins are derived from the first allowed Web origin. The RP ID must be a domain name without scheme or port; IP addresses are invalid. Local HTTP development must use `localhost`, while production requires an HTTPS domain. Changing the RP ID after passkeys are enrolled makes those credentials unusable.

## Create a server and enrollment token

```http
POST /api/v1/servers
Content-Type: application/json

{"name":"Hong Kong VPS"}
```

## Create a global Cloudflare R2 storage channel

```http
POST /api/v1/repositories
Content-Type: application/json

{
  "provider": "cloudflare_r2",
  "name": "R2 Main",
  "url": "s3:https://ACCOUNT_ID.r2.cloudflarestorage.com/bucket/vaultmesh",
  "password": "a-unique-restic-repository-password",
  "environment": {
    "AWS_ACCESS_KEY_ID": "...",
    "AWS_SECRET_ACCESS_KEY": "...",
    "AWS_DEFAULT_REGION": "auto"
  },
  "options": {
    "s3.bucket-lookup": "path"
  }
}
```

Storage channels are global and are not bound to a server. When a project is delivered to an Agent, the Control Plane appends `/<server-id>` to the base URL so each server gets an isolated Restic repository path. Passwords, environment credentials, and approved backend options are AES-256-GCM encrypted before being written to the metadata store, and the response never returns them. Supported provider identifiers and exact fields are documented in [Storage providers](./STORAGE_PROVIDERS.md).

## Create a file project

```http
POST /api/v1/projects
Content-Type: application/json

{
  "server_id": "srv_example",
  "repository_id": "repo_example",
  "name": "System configuration",
  "sources": [
    {
      "type": "files",
      "paths": ["/etc", "/opt/app"],
      "excludes": ["/opt/app/cache/**"],
      "required": true
    }
  ],
  "schedule": {
    "cron": "0 2 * * *",
    "timezone": "Asia/Shanghai",
    "jitter_seconds": 300,
    "max_runtime_seconds": 21600,
    "missed_run_policy": "skip",
    "concurrency_policy": "forbid"
  },
  "policy": {
    "backup": {
      "one_file_system": true,
      "exclude_caches": true,
      "exclude_if_present": [".nobackup"],
      "exclude_larger_than": "2G"
    },
    "retention": {
      "enabled": true,
      "keep_last": 3,
      "keep_hourly": 0,
      "keep_daily": 7,
      "keep_weekly": 4,
      "keep_monthly": 12,
      "keep_yearly": 3,
      "prune": false
    },
    "verification": {
      "mode": "subset",
      "read_data_subset": "1%"
    }
  }
}
```

## Create a MySQL logical-backup project

Use a dedicated database user with the minimum privileges required by `mysqldump`.

```json
{
  "server_id": "srv_example",
  "repository_id": "repo_example",
  "name": "Application database",
  "sources": [
    {
      "type": "mysql",
      "required": true,
      "database": {
        "host": "127.0.0.1",
        "port": 3306,
        "username": "vaultmesh_backup",
        "password": "...",
        "database": "application"
      }
    }
  ],
  "schedule": {
    "cron": "30 2 * * *",
    "timezone": "UTC",
    "jitter_seconds": 120,
    "max_runtime_seconds": 21600,
    "missed_run_policy": "skip",
    "concurrency_policy": "forbid"
  }
}
```

The password is replaced by AES-GCM ciphertext before the project JSON is persisted. The Agent writes a root-only temporary client option file, performs a single-transaction logical dump, backs up the artifact with Restic, and removes the staging directory.

`policy.backup` maps directly to Restic backup options. After a successful snapshot, retention is scoped by both Agent host and `vaultmesh.project_id`; optional verification then checks the repository. A post-backup maintenance failure returns `partial` while preserving the snapshot ID. See [backup project policies](./BACKUP_PROJECTS.md) for the complete contract.

Pause or resume a project without deleting its history:

```http
PATCH /api/v1/projects/{project_id}
Content-Type: application/json

{"enabled": false}
```

For PostgreSQL, use `"type": "postgresql"` with the same `database` object. The Agent uses `pg_dump --format=custom` and a protected temporary password file.

## Add Docker containers and mounted volumes

```json
{
  "server_id": "srv_example",
  "repository_id": "repo_example",
  "name": "Container data",
  "sources": [
    {
      "type": "docker",
      "required": true,
      "docker": {
        "containers": ["application", "redis"],
        "include_volumes": true
      }
    }
  ],
  "schedule": {
    "cron": "0 3 * * *",
    "timezone": "Asia/Shanghai",
    "jitter_seconds": 300,
    "max_runtime_seconds": 21600,
    "missed_run_policy": "skip",
    "concurrency_policy": "forbid"
  }
}
```

The Agent runs `docker inspect` for the explicitly configured containers, stores a sanitized manifest without container environment variables, and adds bind-mount and named-volume host paths to Restic. It does not stop containers or back up their writable layers. Database containers should use the MySQL/PostgreSQL source adapters for application-consistent logical dumps.

`sources` accepts multiple entries. A project may therefore combine application files, Docker mounts and database dumps in the same Restic snapshot. Every project response also includes a server-calculated `next_run_at` value based on its five-field Cron expression and IANA timezone:

```json
{
  "sources": [
    { "type": "files", "paths": ["/opt/application"], "required": true },
    {
      "type": "mysql",
      "required": true,
      "database": {
        "host": "127.0.0.1",
        "port": 3306,
        "username": "vaultmesh_backup",
        "password": "...",
        "database": "application"
      }
    }
  ],
  "schedule": {
    "cron": "30 2 * * *",
    "timezone": "Asia/Shanghai",
    "jitter_seconds": 300,
    "max_runtime_seconds": 21600,
    "missed_run_policy": "skip",
    "concurrency_policy": "forbid"
  }
}
```
