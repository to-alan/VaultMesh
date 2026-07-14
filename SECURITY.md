# Security Policy

VaultMesh controls backup credentials and executes privileged backup operations on enrolled hosts. Treat every deployment as security-sensitive.

## Supported versions

The project is currently pre-release. Only the latest commit on the default branch receives security fixes until the first tagged release.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub private vulnerability reporting after the repository enables it, or contact the repository owner privately.

Please include the affected component, reproduction steps, expected impact, and whether credentials or backup data may have been exposed. Do not include real secrets or user backup data.

## Deployment requirements

- Expose the control plane only through HTTPS.
- Serve the Web console and API as separate origins, and list only exact Web origins in `VAULTMESH_ALLOWED_ORIGINS`; never use a wildcard origin.
- Keep `VAULTMESH_MASTER_KEY` separate from the PostgreSQL backup.
- Treat a global storage channel as a trust boundary: scope its token to the minimum Bucket, do not share it across mutually untrusted servers, and use separate channels when credential isolation is required. VaultMesh automatically isolates Restic paths by server ID but cannot reduce the permissions of the underlying S3 token.
- Remove the one-time enrollment token from the Agent environment after enrollment.
- Restrict the Agent state and staging directories to root.
- Do not mount the Docker socket unless its root-equivalent security impact is understood.

Docker sources use the local Docker CLI and therefore inherit the Agent service account's Docker daemon privileges. The stored Docker manifest intentionally omits container environment variables, but mounted application files may still contain secrets and remain protected only by Restic encryption and access controls.

Restic encryption protects confidentiality but does not prevent a client with delete permission from deleting repository objects. The current direct-S3 mode is not an immutable-backup guarantee.
