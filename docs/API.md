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

The API service does not serve the Web application. Configure the independently deployed, same-site Web origin in `VAULTMESH_ALLOWED_ORIGINS` and set the Web container's `VAULTMESH_API_BASE_URL` to the browser-visible API URL. Origins are matched exactly, credentialed CORS permits `GET`, `POST`, `PATCH`, and `OPTIONS` only for those origins, and wildcard CORS is intentionally unsupported.

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

Repository URLs must contain only the endpoint and path. Embedded user information, passwords, query parameters, and fragments are rejected for S3 and REST URLs; SFTP permits the username but rejects an embedded password. Put credentials in the provider's encrypted credential fields instead.

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
      "mode": "count",
      "keep_last": 14,
      "keep_hourly": 0,
      "keep_daily": 7,
      "keep_weekly": 4,
      "keep_monthly": 12,
      "keep_yearly": 3,
      "keep_within": "",
      "prune": false
    },
    "verification": {
      "mode": "subset",
      "read_data_subset": "1%"
    },
    "maintenance": {
      "separate": true,
      "timezone": "Asia/Shanghai",
      "retention_cron": "30 3 * * *",
      "prune_cron": "0 4 * * 0",
      "verification_cron": "0 5 * * 0"
    }
  }
}
```

`policy.retention.mode` 支持：

- `count`：仅使用 `keep_last`，表示当前项目最多保留最近 N 份；
- `smart`：每日 7 天、每周 1 个月、每月 1 年；
- `gfs`：使用 `keep_last/hourly/daily/weekly/monthly/yearly`；
- `age`：使用 `keep_within`，例如 `90d`、`6m`、`1y6m`。

创建项目后可调用 `POST /api/v1/projects/{projectID}/retention-preview`。Control Plane 会向对应 Agent 投递只读任务，Agent 执行 Restic `forget --dry-run --json`；结果以 `stats.operation = retention_preview` 写入运行记录，包含 `snapshots_kept` 与 `snapshots_removed`。

新项目应设置 `policy.maintenance.separate = true`。启用保留时必须提供 `retention_cron`；启用 Prune 或仓库校验时分别提供 `prune_cron`、`verification_cron`。三个任务共享项目的仓库互斥锁与最长运行时间，但不再处于备份成功路径内。缺少 `maintenance` 的旧项目继续使用备份后维护语义，避免升级后静默停止原有策略。

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

`policy.backup` maps directly to Restic backup options. For new projects, retention is scoped by Agent host and `vaultmesh.project_id`, while Forget, Prune, and optional verification run in independent maintenance windows. Legacy projects without `maintenance.separate` retain their original post-backup behavior. See [backup project policies](./BACKUP_PROJECTS.md) for the complete contract.

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

The current scheduler accepts only `"missed_run_policy":"skip"`. A future `run_once` policy requires a persisted missed-run cursor and additional idempotency semantics and is intentionally rejected instead of being silently ignored.

## Snapshot index and safe restore

Refresh one project's cached Restic inventory:

```http
POST /api/v1/projects/{project_id}/snapshots/refresh
```

The call returns `202 Accepted`. The target Agent executes `restic snapshots --json` scoped by Agent host and project tag; completed metadata is available from:

```http
GET /api/v1/snapshots?project_id={project_id}&limit=200
```

Snapshot commands require the full 64-character Restic ID already present in the cached project inventory:

```http
POST /api/v1/projects/{project_id}/snapshots/{snapshot_id}/protect
Content-Type: application/json

{"protected": true}
```

```http
POST /api/v1/projects/{project_id}/snapshots/{snapshot_id}/browse
Content-Type: application/json

{"path": "/etc/nginx"}
```

```http
POST /api/v1/projects/{project_id}/snapshots/{snapshot_id}/restore
Content-Type: application/json

{"path": "/etc/nginx/nginx.conf"}
```

All three calls are asynchronous and return a leased Agent command. Browse results are written to a Run with `stats.operation = snapshot_browse` and an `entries` array. A restore Run includes `restore_target`, file counts and byte counts. Restore always creates `VAULTMESH_RESTORE_ROOT/<command-id>` on the project's Agent and invokes Restic with `--overwrite never`; the API does not support restoring directly over the source path.

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
