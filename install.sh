#!/bin/sh
set -eu

REPOSITORY_URL=${VAULTMESH_REPOSITORY_URL:-https://github.com/to-alan/VaultMesh.git}
INSTALL_DIR=${VAULTMESH_INSTALL_DIR:-/opt/vaultmesh}
ADMIN_USERNAME=${VAULTMESH_ADMIN_USERNAME:-admin}
# Public host used by browsers to reach this control plane. Priority:
# explicit VAULTMESH_PUBLIC_HOST, then the detected public IP, then the
# first non-loopback interface address, then localhost.
VAULTMESH_PUBLIC_HOST=${VAULTMESH_PUBLIC_HOST:-}
# Version of the prebuilt GHCR images to deploy. Pin a release tag such as
# v0.1.0 in production; "latest" follows the most recent release.
VAULTMESH_IMAGE_TAG=${VAULTMESH_IMAGE_TAG:-latest}

fail() {
	printf 'VaultMesh 安装失败：%s\n' "$1" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "缺少命令 $1"
}

if [ "$(id -u)" -ne 0 ]; then
	fail "请使用 root 运行，推荐：curl -fsSL https://raw.githubusercontent.com/to-alan/VaultMesh/main/install.sh | sudo sh"
fi

case "$ADMIN_USERNAME" in
	""|*[!A-Za-z0-9._-]*) fail "管理员用户名只能包含字母、数字、点、下划线和连字符" ;;
esac

require_command git
require_command openssl
require_command docker
docker compose version >/dev/null 2>&1 || fail "需要 Docker Compose v2（docker compose）"

detect_public_host() {
	if [ -n "$VAULTMESH_PUBLIC_HOST" ]; then
		printf '%s\n' "$VAULTMESH_PUBLIC_HOST"
		return
	fi
	# Best-effort public IP discovery; a NAT or firewalled host may block it.
	for service in https://api.ipify.org https://ifconfig.me/ip; do
		if command -v curl >/dev/null 2>&1; then
			candidate=$(curl -fsS --max-time 4 "$service" 2>/dev/null | tr -d '[:space:]')
		elif command -v wget >/dev/null 2>&1; then
			candidate=$(wget -qO- --timeout=4 "$service" 2>/dev/null | tr -d '[:space:]')
		else
			break
		fi
		case "$candidate" in
			[0-9]*.[0-9]*.[0-9]*.[0-9]*) printf '%s\n' "$candidate"; return ;;
		esac
	done
	# Fall back to the first non-loopback IPv4 address.
	if [ -r /proc/net/fib_trie ] && command -v awk >/dev/null 2>&1; then
		candidate=$(hostname -I 2>/dev/null | awk '{print $1}')
	fi
	case "$candidate" in
		""|127.*|0.0.0.0) printf 'localhost\n' ;;
		*) printf '%s\n' "$candidate" ;;
	esac
}

PUBLIC_HOST=$(detect_public_host)
PUBLIC_API_URL="http://${PUBLIC_HOST}:8080"
ALLOWED_ORIGIN="http://${PUBLIC_HOST}:3000"

if [ -d "$INSTALL_DIR/.git" ]; then
	printf '更新 VaultMesh：%s\n' "$INSTALL_DIR"
	git -C "$INSTALL_DIR" pull --ff-only
elif [ -e "$INSTALL_DIR" ] && [ -n "$(ls -A "$INSTALL_DIR" 2>/dev/null)" ]; then
	fail "$INSTALL_DIR 已存在且不是 VaultMesh Git 仓库，请换一个 VAULTMESH_INSTALL_DIR"
else
	printf '安装 VaultMesh：%s\n' "$INSTALL_DIR"
	mkdir -p "$(dirname "$INSTALL_DIR")"
	git clone --depth 1 "$REPOSITORY_URL" "$INSTALL_DIR"
fi

generated_credentials=false
if [ ! -f "$INSTALL_DIR/.env" ]; then
	umask 077
	ADMIN_PASSWORD=$(openssl rand -hex 16)
	POSTGRES_PASSWORD=$(openssl rand -hex 24)
	MASTER_KEY=$(openssl rand -base64 32)
	cat >"$INSTALL_DIR/.env" <<EOF
VAULTMESH_MASTER_KEY=$MASTER_KEY
VAULTMESH_ADMIN_USERNAME=$ADMIN_USERNAME
VAULTMESH_ADMIN_PASSWORD=$ADMIN_PASSWORD
VAULTMESH_COOKIE_SECURE=false
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
VAULTMESH_API_PORT=8080
VAULTMESH_WEB_PORT=3000
VAULTMESH_PUBLIC_API_URL=$PUBLIC_API_URL
VAULTMESH_ALLOWED_ORIGINS=$ALLOWED_ORIGIN
VAULTMESH_IMAGE_TAG=$VAULTMESH_IMAGE_TAG
VAULTMESH_BIND=${VAULTMESH_BIND:-0.0.0.0}
EOF
	chmod 600 "$INSTALL_DIR/.env"
	generated_credentials=true
else
	printf '保留现有配置：%s/.env\n' "$INSTALL_DIR"
	if ! grep -q '^VAULTMESH_ADMIN_USERNAME=.' "$INSTALL_DIR/.env"; then
		printf 'VAULTMESH_ADMIN_USERNAME=%s\n' "$ADMIN_USERNAME" >>"$INSTALL_DIR/.env"
	fi
	if ! grep -q '^VAULTMESH_ADMIN_PASSWORD=.' "$INSTALL_DIR/.env"; then
		ADMIN_PASSWORD=$(openssl rand -hex 16)
		printf 'VAULTMESH_ADMIN_PASSWORD=%s\n' "$ADMIN_PASSWORD" >>"$INSTALL_DIR/.env"
		generated_credentials=true
	fi
	if ! grep -q '^VAULTMESH_COOKIE_SECURE=.' "$INSTALL_DIR/.env"; then
		printf 'VAULTMESH_COOKIE_SECURE=false\n' >>"$INSTALL_DIR/.env"
	fi
	# Releases before v0.1.0 built images locally; point existing deployments
	# at the prebuilt GHCR images while preserving everything else.
	if ! grep -q '^VAULTMESH_IMAGE_TAG=' "$INSTALL_DIR/.env"; then
		printf 'VAULTMESH_IMAGE_TAG=%s\n' "$VAULTMESH_IMAGE_TAG" >>"$INSTALL_DIR/.env"
	fi
	# WebAuthn RP IDs must be domain strings. Migrate the former loopback-IP
	# defaults while preserving any custom deployment values.
	sed -i 's|^VAULTMESH_PUBLIC_API_URL=http://127\.0\.0\.1:8080$|VAULTMESH_PUBLIC_API_URL=http://localhost:8080|' "$INSTALL_DIR/.env"
	sed -i 's|^VAULTMESH_ALLOWED_ORIGINS=http://127\.0\.0\.1:3000$|VAULTMESH_ALLOWED_ORIGINS=http://localhost:3000|' "$INSTALL_DIR/.env"
	sed -i 's|^VAULTMESH_WEBAUTHN_RP_ID=127\.0\.0\.1$|VAULTMESH_WEBAUTHN_RP_ID=localhost|' "$INSTALL_DIR/.env"
	sed -i 's|^VAULTMESH_WEBAUTHN_RP_ORIGINS=http://127\.0\.0\.1:3000$|VAULTMESH_WEBAUTHN_RP_ORIGINS=http://localhost:3000|' "$INSTALL_DIR/.env"
	# Migrate loopback browser/API URLs to the detected public host so a
	# public deployment is reachable without hand-editing .env. Explicit
	# https:// or domain values are left untouched.
	if [ "$PUBLIC_HOST" != "localhost" ]; then
		sed -i "s|^VAULTMESH_PUBLIC_API_URL=http://localhost:8080$|VAULTMESH_PUBLIC_API_URL=$PUBLIC_API_URL|" "$INSTALL_DIR/.env"
		sed -i "s|^VAULTMESH_ALLOWED_ORIGINS=http://localhost:3000$|VAULTMESH_ALLOWED_ORIGINS=$ALLOWED_ORIGIN|" "$INSTALL_DIR/.env"
	fi
	chmod 600 "$INSTALL_DIR/.env"
fi

# Prebuilt GHCR images avoid a full Go and Node build on the target host.
# `--pull missing` fetches them on first use; when the registry is
# unreachable (private network, rate limit), fall back to building from the
# cloned source so the one-liner keeps working everywhere.
if docker compose --file "$INSTALL_DIR/compose.yaml" --project-directory "$INSTALL_DIR" pull control web >/dev/null 2>&1; then
	printf '拉取预构建镜像（tag %s）…\n' "$(grep '^VAULTMESH_IMAGE_TAG=' "$INSTALL_DIR/.env" | cut -d= -f2)"
	docker compose --file "$INSTALL_DIR/compose.yaml" --project-directory "$INSTALL_DIR" up -d
else
	printf '预构建镜像不可用，回退为本地构建（需要几分钟）…\n'
	docker compose --file "$INSTALL_DIR/compose.yaml" --project-directory "$INSTALL_DIR" up -d --build
fi

printf '\nVaultMesh 已启动。\n'
bind=$(grep '^VAULTMESH_BIND=' "$INSTALL_DIR/.env" 2>/dev/null | cut -d= -f2 || true)
bind=${bind:-0.0.0.0}
if [ "$bind" = "127.0.0.1" ] || [ "$bind" = "localhost" ]; then
	printf 'Web：http://localhost:3000（仅回环，可用 SSH 隧道访问）\n'
	printf 'API：http://localhost:8080\n'
else
	printf 'Web：http://localhost:3000\n'
	printf 'API：http://localhost:8080\n'
	if [ "$PUBLIC_HOST" != "localhost" ]; then
		printf '公网访问：http://%s:3000\n' "$PUBLIC_HOST"
	fi
	printf '请确认防火墙/安全组已放行 %s 与 %s 端口。\n' "${VAULTMESH_WEB_PORT:-3000}" "${VAULTMESH_API_PORT:-8080}"
	printf '浏览器/API 地址已写入 .env（VAULTMESH_PUBLIC_API_URL、VAULTMESH_ALLOWED_ORIGINS）；\n'
	printf '更换域名或 IP 后请同步修改这两个值并重启。\n'
fi
if [ "$generated_credentials" = true ]; then
	printf '用户名：%s\n' "$ADMIN_USERNAME"
	printf '密码：%s\n' "$ADMIN_PASSWORD"
	printf '凭据保存在 %s/.env（权限 0600），请立即安全保存。\n' "$INSTALL_DIR"
else
	printf '继续使用 %s/.env 中已有的管理员账号和密码。\n' "$INSTALL_DIR"
fi
printf '\n当前未配置 HTTPS：控制台可正常访问，但备份与同步操作已被禁用。\n'
printf '配置域名与 HTTPS 反向代理后，在 .env 中将 VAULTMESH_PUBLIC_API_URL 改为 https:// 地址\n'
printf '（并同步更新 VAULTMESH_ALLOWED_ORIGINS、VAULTMESH_COOKIE_SECURE=true），重启即解锁。\n'
