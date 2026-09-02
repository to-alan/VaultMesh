#!/bin/sh
# VaultMesh installer.
#
# Control plane (default):
#   curl -fsSL https://raw.githubusercontent.com/to-alan/VaultMesh/main/install.sh | sudo sh
#
# Backup agent (on the machine to be backed up):
#   curl -fsSL .../install.sh | sudo sh -s -- install-agent <server-url> <enroll-token> [name]
#
# Environment overrides:
#   VAULTMESH_INSTALL_DIR      control-plane directory (default /opt/vaultmesh)
#   VAULTMESH_PUBLIC_HOST      public host/IP of the control plane
#   VAULTMESH_IMAGE_TAG        prebuilt image tag (default latest)
#   VAULTMESH_AGENT_VERSION    agent release tag (default: latest release)
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
VAULTMESH_AGENT_VERSION=${VAULTMESH_AGENT_VERSION:-latest}
GITHUB_RAW_BASE=https://raw.githubusercontent.com/to-alan/VaultMesh

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

# install_agent <server-url> <enroll-token> [server-name]
# Installs the agent binary, systemd unit, and environment file, then starts
# the service. Works on hosts that only see this script (no git checkout).
install_agent() {
	server_url=$1
	token=$2
	server_name=${3:-}

	[ "$(id -u)" -eq 0 ] || fail "请使用 root 运行：curl -fsSL $GITHUB_RAW_BASE/main/install.sh | sudo sh -s -- install-agent <server-url> <token>"
	require_command curl
	require_command systemctl
	require_command docker

	# The agent client refuses plain HTTP unless the control plane is on
	# loopback; surface that rule with actionable wording.
	case "$server_url" in
		http://localhost:*|http://localhost|http://127.0.0.1:*|http://127.0.0.1) ;;
		http://*)
			# Same-host deployments are the common trial path: the local
			# control plane is reachable on loopback, so offer the rewrite.
			if curl -fsS --max-time 3 http://localhost:8080/healthz >/dev/null 2>&1 && [ "$server_url" = "http://$(hostname -I 2>/dev/null | awk '{print $1}'):8080" ]; then
				fail "检测到控制面与本机同宿主。请改用：install-agent 'http://localhost:8080' <token>\n长期部署建议为控制面配置 HTTPS 域名。"
			fi
			fail "Agent 拒绝非 localhost 的 http 控制面地址（明文凭据不可经公网传输）。\n· 与控制面同机？使用 http://localhost:8080\n· 跨机器？先为控制面配置 HTTPS 域名"
			;;
		https://*) ;;
		*) fail "控制面地址必须是 http(s):// URL" ;;
	esac

	arch=$(uname -m)
	case "$arch" in
		x86_64) asset_arch=amd64 ;;
		aarch64|arm64) asset_arch=arm64 ;;
		armv7l|armv6l) asset_arch=armv7 ;;
		*) fail "不支持的架构：$arch" ;;
	esac
	if [ "$VAULTMESH_AGENT_VERSION" = "latest" ]; then
		asset_version=$(curl -fsSL --max-time 10 https://api.github.com/repos/to-alan/VaultMesh/releases/latest | grep '"tag_name"' | cut -d'"' -f4) || true
	fi
	asset_version=${asset_version:-$VAULTMESH_AGENT_VERSION}
	[ -n "$asset_version" ] || fail "无法确定 Agent 版本；请设置 VAULTMESH_AGENT_VERSION（如 v0.1.1 或 edge）"

	if [ "$asset_version" = "edge" ]; then
		# Edge tracks main and contains unreleased features (e.g. detection).
		# The binary is extracted from the prebuilt GHCR image.
		image="ghcr.io/to-alan/vaultmesh/vaultmesh-agent:edge"
		printf '从 edge 镜像提取 Agent（跟踪 main 分支）…\n'
		docker pull "$image" || fail "拉取 $image 失败"
		docker rm -f vaultmesh-agent-extract >/dev/null 2>&1 || true
		docker create --name vaultmesh-agent-extract "$image" >/dev/null || fail "创建提取容器失败"
		docker cp vaultmesh-agent-extract:/vaultmesh-agent /tmp/vaultmesh-agent || fail "提取二进制失败"
		docker rm vaultmesh-agent-extract >/dev/null
	else
		asset_url="https://github.com/to-alan/VaultMesh/releases/download/${asset_version}/vaultmesh-agent-linux-${asset_arch}"
		printf '下载 Agent %s（linux/%s）…\n' "$asset_version" "$asset_arch"
		curl -fsSL --max-time 120 -o /tmp/vaultmesh-agent "$asset_url" || fail "下载失败：$asset_url"
		curl -fsSL --max-time 30 -o /tmp/vaultmesh-agent.sha256 "${asset_url}.sha256" || true
		if [ -f /tmp/vaultmesh-agent.sha256 ]; then
			expected=$(cut -d' ' -f1 /tmp/vaultmesh-agent.sha256)
			actual=$(sha256sum /tmp/vaultmesh-agent | cut -d' ' -f1)
			[ "$expected" = "$actual" ] || fail "SHA256 校验不匹配"
			printf 'SHA256 校验通过。\n'
		fi
	fi

	printf '设置 systemd 服务…\n'
	install -m 0755 /tmp/vaultmesh-agent /usr/local/bin/vaultmesh-agent
	agent_env_url="$GITHUB_RAW_BASE/${asset_version}/deploy/systemd/vaultmesh-agent.env.example"
	curl -fsSL --max-time 30 -o /tmp/vaultmesh-agent.env.example "$agent_env_url" \
		|| curl -fsSL --max-time 30 -o /tmp/vaultmesh-agent.env.example "$GITHUB_RAW_BASE/main/deploy/systemd/vaultmesh-agent.env.example" \
		|| printf '# VaultMesh Agent environment\nVAULTMESH_SERVER_URL=\n' > /tmp/vaultmesh-agent.env.example
	install -m 0644 /tmp/vaultmesh-agent.env.example /etc/vaultmesh-agent.env
	curl -fsSL --max-time 30 -o /etc/systemd/system/vaultmesh-agent.service "$GITHUB_RAW_BASE/main/deploy/systemd/vaultmesh-agent.service" \
		|| fail "下载 systemd unit 失败"

	# A state file from a previous enrollment binds the agent to another
	# identity and makes the new token unusable; reset it automatically.
	if [ -f /var/lib/vaultmesh-agent/state.json ]; then
		printf '检测到旧注册，重置设备身份（控制台里对应的服务器记录可归档）。\n'
	fi

	# Enrollment data goes into the root-only env file and is stripped after a
	# successful start, mirroring the documented manual procedure.
	{
		printf 'VAULTMESH_SERVER_URL=%s\n' "$server_url"
		printf 'VAULTMESH_ENROLLMENT_TOKEN=%s\n' "$token"
	} >> /etc/vaultmesh-agent.env
	chmod 600 /etc/vaultmesh-agent.env

	# Stop any running instance and clear the previous device identity so the
	# new enrollment token can bind cleanly.
	systemctl disable --now vaultmesh-agent >/dev/null 2>&1 || true
	rm -rf /var/lib/vaultmesh-agent/state.json
	# Restore artifacts are user data from recovery tests and are preserved.

	systemctl daemon-reload
	systemctl enable --now vaultmesh-agent >/dev/null 2>&1 || systemctl restart vaultmesh-agent
	sleep 3
	if ! systemctl is-active --quiet vaultmesh-agent; then
		journalctl -u vaultmesh-agent --no-pager -n 30 >&2 || true
		fail "Agent 启动失败，请检查上方日志（常见原因：令牌过期或已使用、控制面地址不可达）"
	fi

	# Registration succeeded: the token is single-use and must not linger.
	sed -i '/^VAULTMESH_ENROLLMENT_TOKEN=/d' /etc/vaultmesh-agent.env
	printf '\nVaultMesh Agent 已安装并注册成功。\n'
	printf '版本：%s\n' "$(vaultmesh-agent --version 2>/dev/null | head -1 || echo "$asset_version")"
	printf '打开控制台，在该服务器下创建备份项目；或使用「探测可备份项」自动发现。\n'
}

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

# uninstall_agent removes the agent service, binary, config, and device
# identity. Restore artifacts under /var/lib/vaultmesh-agent/restores are
# preserved and their location printed.
uninstall_agent() {
	[ "$(id -u)" -eq 0 ] || fail "请使用 root 运行"
	printf '卸载 VaultMesh Agent…\n'
	systemctl disable --now vaultmesh-agent >/dev/null 2>&1 || true
	rm -f /etc/systemd/system/vaultmesh-agent.service
	systemctl daemon-reload
	rm -f /usr/local/bin/vaultmesh-agent
	rm -f /etc/vaultmesh-agent.env
	if [ -d /var/lib/vaultmesh-agent/restores ] && [ -n "$(ls -A /var/lib/vaultmesh-agent/restores 2>/dev/null)" ]; then
		printf '恢复测试产物保留在 /var/lib/vaultmesh-agent/restores，确认后可手动删除。\n'
	fi
	rm -rf /var/lib/vaultmesh-agent
	printf 'Agent 已卸载（设备身份已清除，控制台对应记录可归档）。\n'
}

if [ "${1:-}" = "install-agent" ]; then
	[ $# -ge 3 ] || fail "用法：install-agent <server-url> <enroll-token> [名称]"
	install_agent "$2" "$3" "${4:-}"
	exit 0
fi

if [ "${1:-}" = "uninstall-agent" ]; then
	uninstall_agent
	exit 0
fi

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
