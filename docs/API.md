# VaultMesh API quick reference

The API is versioned under `/api/v1`. Administrator endpoints use:

```http
Authorization: Bearer <VAULTMESH_ADMIN_TOKEN>
```

Agent endpoints use the device token returned by one-time enrollment. Secrets are accepted only over HTTPS in production.

## Create a server and enrollment token

```http
POST /api/v1/servers
Content-Type: application/json

{"name":"Hong Kong VPS"}
```

## Create an S3-compatible Restic repository

```http
POST /api/v1/repositories
Content-Type: application/json

{
  "server_id": "srv_example",
  "name": "R2 Main",
  "url": "s3:https://ACCOUNT.r2.cloudflarestorage.com/bucket/server-prefix",
  "password": "a-unique-restic-repository-password",
  "environment": {
    "AWS_ACCESS_KEY_ID": "...",
    "AWS_SECRET_ACCESS_KEY": "...",
    "AWS_DEFAULT_REGION": "auto"
  }
}
```

Repository secrets are AES-256-GCM encrypted before being written to the metadata store. The response never returns them.

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

For PostgreSQL, use `"type": "postgresql"` with the same `database` object. The Agent uses `pg_dump --format=custom` and a protected temporary password file.

`sources` accepts multiple entries. A project may therefore combine application files and one or more database dumps in the same Restic snapshot. Every project response also includes a server-calculated `next_run_at` value based on its five-field Cron expression and IANA timezone:

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
