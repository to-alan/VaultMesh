# VaultMesh API quick reference

The API is versioned under `/api/v1`. Administrator endpoints use:

```http
Authorization: Bearer <VAULTMESH_ADMIN_TOKEN>
```

Agent endpoints use the device token returned by one-time enrollment. Secrets are accepted only over HTTPS in production.

The API service does not serve the Web application. Configure the independently deployed Web origin in `VAULTMESH_ALLOWED_ORIGINS` and set the Web container's `VAULTMESH_API_BASE_URL` to the browser-visible API URL. Origins are matched exactly; wildcard CORS is intentionally unsupported.

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
  }
}
```

Storage channels are global and are not bound to a server. When a project is delivered to an Agent, the Control Plane appends `/<server-id>` to the base URL so each server gets an isolated Restic repository path. Secrets are AES-256-GCM encrypted before being written to the metadata store, and the response never returns them. Use `provider: "s3_compatible"` with the vendor endpoint for MinIO or another compatible service.

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
