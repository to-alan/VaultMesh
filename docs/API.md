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

Password and TOTP failures are limited independently per client. The fifth failed attempt within 15 minutes returns HTTP `429` with code `rate_limited` and a `Retry-After` header; continued failures increase the lockout from 1 minute up to 15 minutes. A successful check clears that stage's failure history. This limiter is bounded and process-local, so it resets when the Control Plane restarts and is not shared by multiple replicas. Production deployments must keep an additional persistent or distributed limit at the reverse proxy/WAF. Forwarded client IP headers are accepted only from a loopback peer; otherwise the direct peer address is used.

Authenticated personal-security endpoints are grouped under `/api/v1/profile`: `GET /profile`, `POST /profile/password`, `/profile/reauthenticate`, `/profile/totp/begin`, `/profile/totp/enable`, `/profile/totp/disable`, `/profile/recovery-codes`, and the `/profile/passkeys/...` registration/deletion ceremonies. TOTP secrets, recovery-code hashes, and WebAuthn credential records are encrypted at rest with `VAULTMESH_MASTER_KEY`. Recovery codes are returned only when created or regenerated.

Adding or deleting a passkey requires an administrator session authenticated within the previous 10 minutes. A stale session receives HTTP `428` with code `reauthentication_required`; call `POST /api/v1/profile/reauthenticate` with `{"password":"...","code":"..."}` and retry. `code` is required only when TOTP is enabled and accepts either a current authenticator code or an unused recovery code. This keeps password and TOTP fields out of the normal passkey workflow while retaining explicit verification for older sessions.

## Audit events

Authenticated administrators can query the latest append-only control-plane audit events:

```http
GET /api/v1/audit-events?limit=200
```

The limit defaults to 100 and is capped at 500. Events cover authentication, Agent enrollment, account security, server/repository/project changes, manual backups, retention previews, snapshot synchronization, protection, browsing and restore requests. Each event contains a controlled action name, actor, outcome, resource reference when available, client IP, HTTP status and timestamp. Request bodies, passwords, TOTP values, recovery codes, repository credentials, database credentials and arbitrary error text are never copied into the audit table. To prevent unauthenticated traffic from turning audit persistence into a write-amplification attack, identical public-endpoint failures are sampled at most once per action, client and HTTP status per minute; successful events and authenticated administrator mutations are not sampled.

Audit persistence is synchronous and uses a short context detached from client cancellation, but it is not transactionally committed with the business mutation. A failed audit insert is emitted as a structured server error without changing an already completed operation. The table is stored in the same PostgreSQL database, so deployments requiring tamper resistance must export it to an independently controlled logging system.

The API service does not serve the Web application. Configure the independently deployed, same-site Web origin in `VAULTMESH_ALLOWED_ORIGINS` and set the Web container's `VAULTMESH_API_BASE_URL` to the browser-visible API URL. Origins are matched exactly, credentialed CORS permits `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, and `OPTIONS` only for those origins, and wildcard CORS is intentionally unsupported.

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
    "grace_seconds": 3600,
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
    "grace_seconds": 3600,
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

Replace an existing project's editable desired state with `PUT /api/v1/projects/{project_id}`. Send the same shape as project creation. `name`, `sources`, `schedule`, and `policy` are editable; `server_id` and `repository_id` are immutable because moving either would change Agent ownership or the existing recovery chain. Create a new project when a move is intentional. Every successful replacement advances the owning server's desired Revision, and the Agent atomically replaces its local scheduler configuration.

Database passwords are write-only. When replacing a project, retain the existing database source `id` and send an empty `database.password` to preserve its encrypted password; a new database source or a source whose type changed must provide a password.

`schedule.grace_seconds` controls the completion grace period and defaults to one hour. VaultMesh derives project health without waiting for an Agent to report an explicit failure:

```http
GET /api/v1/project-health
```

```json
{
  "items": [{
    "project_id": "prj_example",
    "status": "late",
    "last_successful_at": "2026-07-14T18:02:11Z",
    "expected_at": "2026-07-15T18:00:00Z",
    "deadline_at": "2026-07-16T01:05:00Z"
  }]
}
```

The states are `pending`, `healthy`, `late`, `overdue`, `paused`, and `invalid`. `expected_at` is the first scheduled occurrence after the latest successful backup (or project creation before the first success). The deadline is `expected_at + jitter_seconds + max_runtime_seconds + grace_seconds`. This detects a stopped Agent or scheduler even when no failed Run exists. `/api/v1/dashboard` also includes `projects_late` and `projects_overdue`; an `overdue` transition is evaluated by the notification worker.

## Notification channels and incidents

Create a user-defined contact point. The following example uses a generic Webhook; provider-specific fields are listed in [Notification and alerting](./NOTIFICATIONS.md).

```http
POST /api/v1/notification-channels
Content-Type: application/json

{
  "name": "Primary automation",
  "type": "webhook",
  "enabled": true,
  "send_resolved": true,
  "repeat_interval_seconds": 14400,
  "event_types": ["backup_failure", "rpo_overdue"],
  "project_ids": [],
  "config": {
    "url": "https://hooks.example.com/vaultmesh",
    "method": "POST",
    "authorization": "Bearer SECRET",
    "headers": "{\"X-Environment\":\"production\"}"
  }
}
```

`project_ids: []` routes matching events from every project; a non-empty array acts as an allowlist. `repeat_interval_seconds` accepts 300 seconds through 7 days. Supported event types currently are `backup_failure` and `rpo_overdue`.

Channel configuration is encrypted as one AES-GCM payload. API responses set `configured: true`, expose only a safe destination summary and non-secret fields, and never return the URL, token, authorization value, custom headers or SMTP password. On `PUT`, omitted or blank secret fields preserve the encrypted value already stored; changing the channel type requires the new type's required fields.

```http
GET    /api/v1/notification-channels
PUT    /api/v1/notification-channels/{channel_id}
PATCH  /api/v1/notification-channels/{channel_id}  {"enabled":false}
DELETE /api/v1/notification-channels/{channel_id}
POST   /api/v1/notification-channels/{channel_id}/test
```

Archival is soft deletion and prevents future delivery. The test endpoint performs a real outbound request using the saved configuration and returns `204` only when the provider accepts it.

Inspect incident and delivery history:

```http
GET /api/v1/alert-incidents?limit=100
GET /api/v1/notification-deliveries?limit=100
POST /api/v1/alerts/evaluate
```

The final endpoint is an administrator troubleshooting action; normal evaluation runs every 30 seconds. Incidents use a stable fingerprint per project and condition. The first transition queues `firing`, unchanged conditions are suppressed until the configured repeat interval, and a healthy transition queues `resolved` when the channel enables recovery messages. Delivery is asynchronous and persisted in PostgreSQL; failures are attempted at most five times with bounded backoff. An outbound provider failure does not change the backup Run or project-health result.

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
    "grace_seconds": 3600,
    "missed_run_policy": "skip",
    "concurrency_policy": "forbid"
  }
}
```

The Agent runs `docker inspect` for the explicitly configured containers, stores a sanitized manifest without container environment variables, and adds bind-mount and named-volume host paths to Restic. It does not stop containers or back up their writable layers. Database containers should use the MySQL/PostgreSQL source adapters for application-consistent logical dumps.

The current scheduler accepts only `"missed_run_policy":"skip"`. A future `run_once` policy requires a persisted missed-run cursor and additional idempotency semantics and is intentionally rejected instead of being silently ignored.

Agent run reports are accepted only when the run ID, project ID and idempotency key are bounded, scheduled/start times are present and ordered, terminal reports contain a finish time, running reports do not, and start/finish times are not more than five minutes ahead of Control Plane time. Delayed historical reports remain valid. These checks prevent a broken or compromised Agent clock from corrupting run ordering and dashboard state.

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
