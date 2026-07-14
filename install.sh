#!/bin/sh
set -eu

REPOSITORY_URL=${VAULTMESH_REPOSITORY_URL:-https://github.com/to-alan/VaultMesh.git}
INSTALL_DIR=${VAULTMESH_INSTALL_DIR:-/opt/vaultmesh}
ADMIN_USERNAME=${VAULTMESH_ADMIN_USERNAME:-admin}

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
VAULTMESH_PUBLIC_API_URL=http://localhost:8080
VAULTMESH_ALLOWED_ORIGINS=http://localhost:3000
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
	# WebAuthn RP IDs must be domain strings. Migrate the former loopback-IP
	# defaults while preserving any custom deployment values.
	sed -i 's|^VAULTMESH_PUBLIC_API_URL=http://127\.0\.0\.1:8080$|VAULTMESH_PUBLIC_API_URL=http://localhost:8080|' "$INSTALL_DIR/.env"
	sed -i 's|^VAULTMESH_ALLOWED_ORIGINS=http://127\.0\.0\.1:3000$|VAULTMESH_ALLOWED_ORIGINS=http://localhost:3000|' "$INSTALL_DIR/.env"
	sed -i 's|^VAULTMESH_WEBAUTHN_RP_ID=127\.0\.0\.1$|VAULTMESH_WEBAUTHN_RP_ID=localhost|' "$INSTALL_DIR/.env"
	sed -i 's|^VAULTMESH_WEBAUTHN_RP_ORIGINS=http://127\.0\.0\.1:3000$|VAULTMESH_WEBAUTHN_RP_ORIGINS=http://localhost:3000|' "$INSTALL_DIR/.env"
	chmod 600 "$INSTALL_DIR/.env"
fi

printf '构建并启动 VaultMesh…\n'
docker compose --file "$INSTALL_DIR/compose.yaml" --project-directory "$INSTALL_DIR" up -d --build

printf '\nVaultMesh 已启动。\n'
printf 'Web：http://localhost:3000\n'
printf 'API：http://localhost:8080\n'
if [ "$generated_credentials" = true ]; then
	printf '用户名：%s\n' "$ADMIN_USERNAME"
	printf '密码：%s\n' "$ADMIN_PASSWORD"
	printf '凭据保存在 %s/.env（权限 0600），请立即安全保存。\n' "$INSTALL_DIR"
else
	printf '继续使用 %s/.env 中已有的管理员账号和密码。\n' "$INSTALL_DIR"
fi
printf '远程服务器请使用 SSH 端口转发访问，生产环境启用 HTTPS 后再对公网开放。\n'
