# Security Policy

VaultMesh controls backup credentials and executes privileged backup operations on enrolled hosts. Treat every deployment as security-sensitive.

## Supported versions

The project is currently pre-release. Only the latest commit on the default branch receives security fixes until the first tagged release.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub private vulnerability reporting after the repository enables it, or contact the repository owner privately.

Please include the affected component, reproduction steps, expected impact, and whether credentials or backup data may have been exposed. Do not include real secrets or user backup data.

## Deployment requirements

- Expose the control plane only through HTTPS.
- Keep `VAULTMESH_MASTER_KEY` separate from the PostgreSQL backup.
- Use a unique repository and storage credential scope for each server.
- Remove the one-time enrollment token from the Agent environment after enrollment.
- Restrict the Agent state and staging directories to root.
- Do not mount the Docker socket unless its root-equivalent security impact is understood.

Restic encryption protects confidentiality but does not prevent a client with delete permission from deleting repository objects. The current direct-S3 mode is not an immutable-backup guarantee.
