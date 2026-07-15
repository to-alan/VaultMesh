# Security Policy

VaultMesh controls backup credentials and executes privileged backup operations on enrolled hosts. Treat every deployment as security-sensitive.

## Supported versions

The project is currently pre-release. Only the latest commit on the default branch receives security fixes until the first tagged release.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub private vulnerability reporting after the repository enables it, or contact the repository owner privately.

Please include the affected component, reproduction steps, expected impact, and whether credentials or backup data may have been exposed. Do not include real secrets or user backup data.

## Deployment requirements

- Expose the control plane only through HTTPS.
- Serve the Web console and API as same-site HTTPS origins, list only exact Web origins in `VAULTMESH_ALLOWED_ORIGINS`, never use a wildcard origin, and set `VAULTMESH_COOKIE_SECURE=true`.
- Protect `.env` as a root-readable secret because it contains the administrator password, master key, and PostgreSQL password. Rotate those values if the file is exposed.
- Administrator login uses a server-side session carried by an HttpOnly, SameSite cookie. The Web application does not store a bearer token. Agent enrollment and device credentials remain separate machine identities and must not be reused as administrator credentials.
- Password and TOTP failures have separate, bounded in-process limits. Five failures within 15 minutes trigger a progressive 1-to-15-minute lockout and a `Retry-After` response. Because this state is neither persistent nor shared between replicas, keep a second rate limit at the reverse proxy or WAF.
- Security-sensitive actions are appended to the PostgreSQL audit trail with actor, action, result, resource reference, source IP, HTTP status, and time. Request bodies and arbitrary error strings are excluded by design so credentials cannot be copied into audit records. Identical unauthenticated failures are sampled once per action, client, and status per minute to prevent audit write amplification.
- Enable TOTP and store the one-time recovery codes outside the VaultMesh host. Adding or deleting a passkey requires a session authenticated within the previous 10 minutes; stale sessions must reauthenticate with the current password and, when enabled, a fresh second factor.
- Keep the WebAuthn RP ID stable and restrict RP origins to the exact HTTPS console origins. A reverse-proxy hostname change requires registering new passkeys before retiring the old hostname.
- Keep `VAULTMESH_MASTER_KEY` separate from the PostgreSQL backup.
- Treat a global storage channel as a trust boundary: scope its token to the minimum Bucket, do not share it across mutually untrusted servers, and use separate channels when credential isolation is required. VaultMesh automatically isolates Restic paths by server ID but cannot reduce the permissions of the underlying S3 token.
- Remove the one-time enrollment token from the Agent environment after enrollment.
- Restrict the Agent state and staging directories to root.
- Do not mount the Docker socket unless its root-equivalent security impact is understood.

Docker sources use the local Docker CLI and therefore inherit the Agent service account's Docker daemon privileges. The stored Docker manifest intentionally omits container environment variables, but mounted application files may still contain secrets and remain protected only by Restic encryption and access controls.

Restic encryption protects confidentiality but does not prevent a client with delete permission from deleting repository objects. The current direct-S3 mode is not an immutable-backup guarantee.

The built-in audit trail shares the Control Plane database and is therefore operational evidence, not a cryptographically tamper-evident archive. Environments with stronger assurance requirements should forward PostgreSQL audit events and structured application logs to an independently administered append-only destination.
