# Security Policy

VaultMesh controls backup credentials and executes privileged backup operations on enrolled hosts. Treat every deployment as security-sensitive.

## Supported versions

The project is currently pre-release. Only the latest commit on the default branch receives security fixes until the first tagged release.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub private vulnerability reporting after the repository enables it, or contact the repository owner privately.

Please include the affected component, reproduction steps, expected impact, and whether credentials or backup data may have been exposed. Do not include real secrets or user backup data.

## Deployment requirements

- Expose the control plane only through HTTPS.
- Prefer same-site HTTPS origins for the Web console and API. List only exact Web origins in `VAULTMESH_ALLOWED_ORIGINS`, never use a wildcard, and set `VAULTMESH_COOKIE_SECURE=true`. A frontend on a different site must additionally use `VAULTMESH_COOKIE_SAME_SITE=none`; the server rejects that mode without Secure cookies.
- The Web container exposes its runtime API URL as inert `/config.txt` data and validates the scheme and URL components before loading the application. Do not replace it with an interpolated JavaScript configuration file; raw environment substitution into executable JavaScript creates a script-injection boundary.
- Protect `.env` as a root-readable secret because it contains the administrator password, master key, and PostgreSQL password. Rotate those values if the file is exposed.
- Administrator login uses a server-side session carried by an HttpOnly, SameSite cookie. The Web application does not store a bearer token. Agent enrollment and device credentials remain separate machine identities and must not be reused as administrator credentials.
- Password and TOTP failures have separate, bounded in-process limits. The same protection covers login and authenticated password/TOTP/recovery-code confirmation; sensitive account operations are keyed by client address and current administrator session. Five failures within 15 minutes trigger a progressive 1-to-15-minute lockout and a `Retry-After` response. Because this state is neither persistent nor shared between replicas, keep a second rate limit at the reverse proxy or WAF.
- Security-sensitive actions are appended to the PostgreSQL audit trail with actor, action, result, resource reference, source IP, HTTP status, and time. Request bodies and arbitrary error strings are excluded by design so credentials cannot be copied into audit records. Identical unauthenticated failures are sampled once per action, client, and status per minute to prevent audit write amplification.
- Enable TOTP and store the one-time recovery codes outside the VaultMesh host. Adding or deleting a passkey requires a session authenticated within the previous 10 minutes; stale sessions must reauthenticate with the current password and, when enabled, a fresh second factor.
- Keep the WebAuthn RP ID stable and restrict RP origins to the exact HTTPS console origins. A reverse-proxy hostname change requires registering new passkeys before retiring the old hostname.
- Keep `VAULTMESH_MASTER_KEY` separate from the PostgreSQL backup.
- Treat a global storage channel as a trust boundary: scope its token to the minimum Bucket, do not share it across mutually untrusted servers, and use separate channels when credential isolation is required. VaultMesh automatically isolates Restic paths by server ID but cannot reduce the permissions of the underlying S3 token.
- Remove the one-time enrollment token from the Agent environment after enrollment.
- Configure the Agent with a bare HTTPS Control Plane origin. The Agent rejects URL credentials, subpaths, query strings and fragments, does not follow redirects, and accepts plain HTTP only on an IP loopback address or `localhost` for local development.
- Restrict the Agent state and staging directories to root.
- Do not mount the Docker socket unless its root-equivalent security impact is understood.
- Notification HTTP and SMTP connections do not use environment proxies, do not follow HTTP redirects, resolve every target address before connecting, and reject private/loopback destinations by default. Enable “允许访问私有网络地址” only for a deliberately self-hosted target. Link-local, multicast, unspecified and cloud metadata destinations remain blocked even with that option enabled. Enforce a second destination allowlist with the host firewall in high-assurance deployments.

Docker sources use the local Docker CLI and therefore inherit the Agent service account's Docker daemon privileges. The stored Docker manifest intentionally omits container environment variables, but mounted application files may still contain secrets and remain protected only by Restic encryption and access controls.

Restic encryption protects confidentiality but does not prevent a client with delete permission from deleting repository objects. The current direct-S3 mode is not an immutable-backup guarantee.

The built-in audit trail shares the Control Plane database and is therefore operational evidence, not a cryptographically tamper-evident archive. Environments with stronger assurance requirements should forward PostgreSQL audit events and structured application logs to an independently administered append-only destination.

## Security design references

- [OWASP CSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html): exact origin validation, SameSite cookies and exclusion of browser simple content types.
- [OWASP SSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html): scheme validation, complete address resolution, redirect refusal and network-layer egress controls.
- [Go `net/http` Client documentation](https://pkg.go.dev/net/http#Client): explicit redirect policy and sensitive-header forwarding behavior.
