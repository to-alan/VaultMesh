#!/bin/sh
# Release installer and installed management command. No git pulls/source builds.
set -eu
umask 077
RELEASE_BASE=https://github.com/to-alan/VaultMesh/releases
INSTALL_DIR=${VAULTMESH_INSTALL_DIR:-/opt/vaultmesh}
VERSION=${VAULTMESH_VERSION:-latest}
MODE=managed
DOMAIN=
PUBLIC_URL=
TMP=
LOCK=
BACKUP=
CONTROL_STOPPED=false
MIGRATION_STARTED=false
AGENT_STOPPED=false

fail() { printf 'VaultMesh：%s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "缺少 $1；请先安装依赖，见 docs/INSTALL.md"; }
has_restic() { command -v restic >/dev/null 2>&1; }
valid_line() { [ "$(printf '%s' "$1" | tr -d '\r\n')" = "$1" ]; }
valid_version() { valid_line "$1" && printf '%s\n' "$1" | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$'; }
valid_domain() { valid_line "$1" && printf '%s\n' "$1" | LC_ALL=C grep -Eq '^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$'; }
valid_url() {
    # Origin only: no userinfo, path, whitespace or environment-file injection.
    valid_line "$1" && printf '%s\n' "$1" | LC_ALL=C grep -Eq '^https://[a-zA-Z0-9.-]+(:[0-9]{1,5})?$|^http://(localhost|127\.0\.0\.1|\[::1\])(:[0-9]{1,5})?$'
}
download() { curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --connect-timeout 15 --max-time 300 --retry 3 --output "$2" "$1"; }
verify_file() {
    checksum=$(awk 'NR==1 {print $1}' "$2")
    printf '%s\n' "$checksum" | grep -Eq '^[a-f0-9]{64}$' || fail '无效的 SHA256 文件'
    actual=$(sha256sum "$1" | awk '{print $1}')
    [ "$checksum" = "$actual" ] || fail "SHA256 校验失败：$(basename "$1")"
}
resolve_version() {
    if [ "$VERSION" = latest ]; then
        latest_url=$(curl -fLsS --proto '=https' --max-time 30 --output /dev/null --write-out '%{url_effective}' "$RELEASE_BASE/latest") || fail '无法查询版本；可用 --version vX.Y.Z 指定'
        VERSION=${latest_url##*/}
    fi
    valid_version "$VERSION" || fail "版本须为 vX.Y.Z 或 vX.Y.Z-rc.N：$VERSION"
}
newer_version() {
    [ "$1" != "$2" ] || return 1
    old_base=${1%%-*}; new_base=${2%%-*}
    if [ "$old_base" = "$new_base" ]; then
        case "$1:$2" in *-rc.*:*-rc.*) [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" = "$2" ];; *-rc.*:*) return 0;; *) return 1;; esac
    else
        [ "$(printf '%s\n%s\n' "$old_base" "$new_base" | sort -V | tail -1)" = "$new_base" ]
    fi
}
fetch_bundle() {
    archive="vaultmesh-deploy-${VERSION}.tar.gz"
    download "$RELEASE_BASE/download/$VERSION/$archive" "$TMP/$archive" || fail '该版本没有部署包（旧版 v0.1.0/v0.1.1 不支持新安装器）；请查看 Releases'
    download "$RELEASE_BASE/download/$VERSION/$archive.sha256" "$TMP/$archive.sha256"
    verify_file "$TMP/$archive" "$TMP/$archive.sha256"
    tar -tzf "$TMP/$archive" > "$TMP/members"
    if grep -Eq '(^/|(^|/)\.\.(/|$))' "$TMP/members"; then fail '发布包含不安全路径'; fi
    tar -tvzf "$TMP/$archive" > "$TMP/types"
    if grep -Ev '^[-d]' "$TMP/types" >/dev/null; then fail '发布包含链接或特殊文件'; fi
    mkdir "$TMP/bundle"
    tar -xzf "$TMP/$archive" -C "$TMP/bundle" --no-same-owner
    [ "$(cat "$TMP/bundle/VERSION")" = "$VERSION" ] || fail '部署包版本不匹配'
    for item in install.sh compose.yaml compose.managed.yaml compose.external.yaml Caddyfile deploy/systemd/vaultmesh-agent.service; do
        [ -f "$TMP/bundle/$item" ] || fail "发布包缺少 $item"
    done
}
read_setting() { sed -n "s/^$1=//p" "$INSTALL_DIR/.env"; }
compose_at() {
    release_dir=$1; shift
    VAULTMESH_RELEASE_DIR="$release_dir" VAULTMESH_IMAGE_TAG="$(cat "$release_dir/VERSION")" \
        docker compose --project-name vaultmesh --project-directory "$INSTALL_DIR" --env-file "$INSTALL_DIR/.env" \
        -f "$release_dir/compose.yaml" -f "$release_dir/compose.$MODE.yaml" "$@" </dev/null
}
current_compose() { compose_at "$INSTALL_DIR/current" "$@"; }
load_installation() {
    [ -f "$INSTALL_DIR/.vaultmesh-installation" ] || fail '不是发布包安装目录；旧版 Git / IP 测试部署请按 docs/UPGRADE.md 迁移，不会覆盖现有文件'
    [ -f "$INSTALL_DIR/current/VERSION" ] || fail '上次安装尚未完成；保留 .env 和数据卷，按 docs/UPGRADE.md 的中断恢复步骤处理'
    MODE=$(read_setting VAULTMESH_PROXY_MODE)
    case "$MODE" in managed|external) ;; *) fail '无效的代理模式';; esac
    PUBLIC_URL=$(read_setting VAULTMESH_PUBLIC_API_URL)
    valid_url "$PUBLIC_URL" || fail '.env 中的公开地址无效'
}
backup_control() {
    BACKUP="$INSTALL_DIR/backups/$(date -u +%Y%m%dT%H%M%SZ)-$(cat "$INSTALL_DIR/current/VERSION")"
    mkdir -p "$BACKUP"
    chmod 700 "$INSTALL_DIR/backups" "$BACKUP"
    [ ! -e "$BACKUP/postgres.dump" ] || fail '同一秒已有备份，请稍后重试'
    cp -p "$INSTALL_DIR/.env" "$BACKUP/.env"
    cp -R "$INSTALL_DIR/current/" "$BACKUP/release"
    if ! current_compose exec -T postgres pg_dump -U vaultmesh -d vaultmesh --format=custom > "$BACKUP/postgres.dump"; then
        fail "数据库备份失败，未启动新版本；不完整文件保留在 $BACKUP"
    fi
    [ -s "$BACKUP/postgres.dump" ] || fail '数据库备份为空'
    # Intentional stdin here; other Compose commands must not consume SSH/script stdin.
    VAULTMESH_RELEASE_DIR="$INSTALL_DIR/current" VAULTMESH_IMAGE_TAG="$(cat "$INSTALL_DIR/current/VERSION")" \
        docker compose -p vaultmesh --project-directory "$INSTALL_DIR" --env-file "$INSTALL_DIR/.env" \
        -f "$INSTALL_DIR/current/compose.yaml" -f "$INSTALL_DIR/current/compose.$MODE.yaml" \
        exec -T postgres pg_restore --list < "$BACKUP/postgres.dump" > "$BACKUP/postgres.list"
    (cd "$BACKUP" && sha256sum postgres.dump .env > SHA256SUMS)
    printf '备份已保存：%s（含主密钥，请加密复制到异机）\n' "$BACKUP"
}
finish() {
    status=$?
    trap - EXIT HUP INT TERM
    if [ "$CONTROL_STOPPED" = true ]; then
        if [ "$MIGRATION_STARTED" = false ]; then
            current_compose start control >/dev/null 2>&1 || printf '请手动启动原控制面。\n' >&2
        else
            compose_at "$TARGET" stop control >/dev/null 2>&1 || true
            printf '升级未完成，控制面已停止；不自动降级数据库。备份：%s。请按 docs/UPGRADE.md 恢复。\n' "$BACKUP" >&2
        fi
    fi
    if [ "$AGENT_STOPPED" = true ]; then
        printf 'Agent 更新未完成；身份/配置已保留，备份：%s。请按 docs/UPGRADE.md 检查后启动，不要重新注册。\n' "$BACKUP" >&2
    fi
    if [ "$status" -ne 0 ] && [ "${ACTION:-}" = install ] && [ -f "$INSTALL_DIR/.vaultmesh-installation" ]; then
        printf '首次安装未完成，配置和数据保留在 %s；请按 docs/UPGRADE.md 的中断恢复步骤处理，不要删除数据卷。\n' "$INSTALL_DIR" >&2
    fi
    if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
    if [ -n "$LOCK" ]; then rmdir "$LOCK" 2>/dev/null || true; fi
    exit "$status"
}
help_text() {
    printf '%s\n' 'VaultMesh 发布包安装与维护（Linux）' \
      '  install --domain backup.example.com [--version vX.Y.Z]' \
      '  install --url https://backup.example.com --proxy external [--version vX.Y.Z]' \
      '  upgrade [--version vX.Y.Z]   校验 → 拉取镜像 → 备份 → 升级 → 健康检查' \
      '  status | logs | backup' \
      '  compose <args...>            使用当前发布包的 Compose 配置' \
      '  install-agent <https-url> <one-time-token> [--version vX.Y.Z]' \
      '  upgrade-agent --version vX.Y.Z   保留设备身份、配置和恢复目录' \
      '  --dir /opt/vaultmesh         自定义控制面目录（每台主机仅一个控制面）' \
      '请预先安装 Docker；不会自动安装 Docker、拉源码或回退到现场编译。'
}
control_action() {
    need docker; need openssl
    docker info >/dev/null 2>&1 || fail 'Docker 不可用，请启动 Docker Engine'
    docker compose version >/dev/null || fail '需要 Docker Compose v2（支持 up --wait）'
    if [ "$ACTION" != install ]; then load_installation; fi
    case "$ACTION" in
        status) printf '版本：%s\n入口：%s\n' "$(cat "$INSTALL_DIR/current/VERSION")" "$PUBLIC_URL"; current_compose ps; return;;
        logs) current_compose logs --tail 100 control gateway; return;;
        backup) backup_control; return;;
    esac
    resolve_version
    if [ "$ACTION" = upgrade ]; then
        current_version=$(cat "$INSTALL_DIR/current/VERSION")
        if [ "$current_version" = "$VERSION" ]; then printf '已经是 %s；检查当前服务。\n' "$VERSION"; current_compose ps; return; fi
        newer_version "$current_version" "$VERSION" || fail '拒绝降级；数据库迁移不保证向后兼容，请按回滚文档恢复'
    else
        [ ! -d "$INSTALL_DIR/.git" ] || fail '检测到旧版 Git 部署，拒绝自动覆盖。请阅读 docs/UPGRADE.md'
        [ -z "$(find "$INSTALL_DIR" -mindepth 1 -maxdepth 1 ! -name .operation-lock -print -quit)" ] || fail '安装目录非空，请使用 upgrade；不覆盖既有配置/数据库'
        # Changing --dir must not adopt an existing Compose project or database.
        existing_containers=$(docker ps -a --filter label=com.docker.compose.project=vaultmesh --format '{{.ID}}') || fail '无法检查现有容器，停止安装'
        [ -z "$existing_containers" ] || fail '本机已有 vaultmesh 容器；换安装目录不能创建第二套或接管旧部署'
        existing_volumes=$(docker volume ls --filter label=com.docker.compose.project=vaultmesh --format '{{.Name}}') || fail '无法检查现有数据卷，停止安装'
        [ -z "$existing_volumes" ] || fail '发现旧 vaultmesh 数据卷，请按迁移文档处理，不自动重新初始化'
        existing_database=$(docker volume ls --filter 'name=^vaultmesh_vaultmesh-postgres$' --format '{{.Name}}') || fail '无法检查 PostgreSQL 数据卷，停止安装'
        [ -z "$existing_database" ] || fail '发现同名 PostgreSQL 数据卷，拒绝用新密码/主密钥接管'
        if [ "$MODE" = managed ]; then
            valid_domain "$DOMAIN" || fail '请用 --domain 指定已解析到本机的域名；IP 测试和已有代理见 docs/INSTALL.md'
            PUBLIC_URL="https://$DOMAIN"; SITE_ADDRESS=$DOMAIN
        else
            valid_url "$PUBLIC_URL" || fail '请用 --url 指定 HTTPS Origin，不要包含路径'
            case "$PUBLIC_URL" in https://*) ;; *) fail '外部代理公开入口必须是 HTTPS';; esac
            DOMAIN=${PUBLIC_URL#https://}; DOMAIN=${DOMAIN%%:*}; SITE_ADDRESS=http://:80
        fi
    fi
    fetch_bundle
    TARGET="$INSTALL_DIR/releases/$VERSION"
    [ ! -e "$TARGET" ] || fail "目标发布目录已存在：${TARGET}；如为失败升级，请先查看恢复文档，不能自动覆盖"
    mkdir -p "$INSTALL_DIR/releases"
    mv "$TMP/bundle" "$TARGET"
    if [ "$ACTION" = install ]; then
        admin_password=$(openssl rand -hex 16)
        {
            printf 'VAULTMESH_PROXY_MODE=%s\nVAULTMESH_SITE_ADDRESS=%s\n' "$MODE" "$SITE_ADDRESS"
            printf 'VAULTMESH_PUBLIC_API_URL=%s\nVAULTMESH_WEBAUTHN_RP_ID=%s\n' "$PUBLIC_URL" "$DOMAIN"
            printf 'VAULTMESH_ADMIN_USERNAME=admin\nVAULTMESH_ADMIN_PASSWORD=%s\n' "$admin_password"
            printf 'POSTGRES_PASSWORD=%s\nVAULTMESH_MASTER_KEY=%s\n' "$(openssl rand -hex 24)" "$(openssl rand -base64 32)"
        } > "$INSTALL_DIR/.env"
        printf '1\n' > "$INSTALL_DIR/.vaultmesh-installation"
    fi
    compose_at "$TARGET" config --quiet
    if [ "$ACTION" = install ]; then
        compose_at "$TARGET" pull
    else
        # Registry failure cannot take a working site offline. PostgreSQL stays put.
        compose_at "$TARGET" pull control web gateway
        CONTROL_STOPPED=true
        current_compose stop control
        backup_control
        MIGRATION_STARTED=true
    fi
    if [ "$ACTION" = install ]; then
        compose_at "$TARGET" up -d --no-build --pull never --wait --wait-timeout 180
    else
        compose_at "$TARGET" up -d --no-deps --no-build --pull never --wait --wait-timeout 180 control web gateway
    fi
    if [ "$MODE" = managed ]; then
        curl -fSs --retry 12 --retry-all-errors --retry-delay 5 --connect-timeout 5 --max-time 10 "$PUBLIC_URL/healthz" >/dev/null || fail 'HTTPS 验证失败：检查 DNS、80/443、防火墙和 gateway 日志'
    else
        curl -fSs --retry 5 --retry-connrefused --max-time 10 http://127.0.0.1:3000/healthz >/dev/null || fail '本地代理未通过健康检查'
    fi
    ln -s "releases/$VERSION" "$INSTALL_DIR/.current-next"
    mv -Tf "$INSTALL_DIR/.current-next" "$INSTALL_DIR/current"
    CONTROL_STOPPED=false
    if [ ! -e /usr/local/bin/vaultmesh ] && [ ! -L /usr/local/bin/vaultmesh ]; then ln -s "$INSTALL_DIR/current/install.sh" /usr/local/bin/vaultmesh; fi
    printf '\nVaultMesh %s 已就绪：%s\n' "$VERSION" "$PUBLIC_URL"
    if [ "$ACTION" = install ]; then printf '账号：admin\n初始密码：%s\n请立即修改密码，并异机备份 .env 和数据库。\n' "$admin_password"; fi
    if [ "$MODE" = external ]; then printf '请让已有 HTTPS 代理转发到 http://127.0.0.1:3000；尚未验证公网证书。\n'; fi
    printf '下次升级：sudo sh %s/current/install.sh upgrade --dir %s\n' "$INSTALL_DIR" "$INSTALL_DIR"
}

agent_action() {
    need systemctl
    case "$(uname -m)" in x86_64) asset_arch=amd64;; aarch64|arm64) asset_arch=arm64;; armv7l) asset_arch=armv7;; *) fail 'Agent 支持 Linux amd64 / arm64 / armv7，不支持 armv6';; esac
    if [ "$ACTION" = install-agent ]; then
        valid_url "$AGENT_URL" || fail 'Agent 地址必须是 HTTPS 或回环 HTTP Origin'
        valid_line "$AGENT_TOKEN" || fail '注册令牌格式无效'
        printf '%s\n' "$AGENT_TOKEN" | grep -Eq '^[A-Za-z0-9_-]+$' || fail '注册令牌格式无效'
        if [ -e /var/lib/vaultmesh-agent/state.json ] || [ -e /etc/vaultmesh-agent.env ] || [ -e /usr/local/bin/vaultmesh-agent ]; then
            fail '已存在 Agent；请使用 upgrade-agent，绝不自动删除设备身份'
        fi
    else
        if [ ! -s /var/lib/vaultmesh-agent/state.json ] || [ ! -f /etc/vaultmesh-agent.env ] || [ ! -x /usr/local/bin/vaultmesh-agent ]; then
            fail '找不到完整的现有 Agent 安装'
        fi
        [ "$VERSION" != latest ] || fail '请用 --version 指定与控制面一致的版本，先升级控制面再升级 Agent'
    fi
    case "$VERSION" in
        edge|edge-*)
            need docker
            valid_line "$VERSION" || fail 'edge 必须是完整提交 SHA'
            printf '%s\n' "$VERSION" | grep -Eq '^edge(-[a-f0-9]{40})?$' || fail 'edge 必须是完整提交 SHA'
            agent_image="ghcr.io/to-alan/vaultmesh/vaultmesh-agent:$VERSION"
            docker pull "$agent_image" </dev/null
            revision=$(docker image inspect "$agent_image" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
            printf '%s\n' "$revision" | grep -Eq '^[a-f0-9]{40}$' || fail '镜像缺少有效提交标签'
            [ "$VERSION" = edge ] || [ "$VERSION" = "edge-$revision" ] || fail 'edge 镜像版本不匹配'
            extract_id=$(docker create "$agent_image")
            if ! docker cp "$extract_id:/vaultmesh-agent" "$TMP/agent"; then docker rm "$extract_id" >/dev/null; fail '提取 Agent 失败'; fi
            docker rm "$extract_id" >/dev/null
            VERSION="edge-$revision"
            download "https://raw.githubusercontent.com/to-alan/VaultMesh/$revision/deploy/systemd/vaultmesh-agent.service" "$TMP/agent.service"
            ;;
        *)
            resolve_version; fetch_bundle
            cp "$TMP/bundle/deploy/systemd/vaultmesh-agent.service" "$TMP/agent.service"
            asset="vaultmesh-agent-linux-$asset_arch"
            download "$RELEASE_BASE/download/$VERSION/$asset" "$TMP/agent"
            download "$RELEASE_BASE/download/$VERSION/$asset.sha256" "$TMP/agent.sha256"
            verify_file "$TMP/agent" "$TMP/agent.sha256"
            ;;
    esac
    chmod 755 "$TMP/agent"
    "$TMP/agent" --version | grep -F "vaultmesh-agent $VERSION (" >/dev/null || fail '二进制版本不匹配'
    if [ "$ACTION" = upgrade-agent ]; then
        old_version=$(/usr/local/bin/vaultmesh-agent --version | awk '{print $2}')
        if valid_version "$old_version" && valid_version "$VERSION"; then
            newer_version "$old_version" "$VERSION" || fail 'Agent 目标不是更新的版本；不会自动降级或重新注册'
        fi
    fi
    # Install essential Restic only if missing. Never replace administrator tools.
    if ! has_restic; then
        need bzip2
        restic_arch=$asset_arch; [ "$restic_arch" != armv7 ] || restic_arch=arm
        restic_asset="restic_0.18.0_linux_$restic_arch.bz2"
        restic_base=https://github.com/restic/restic/releases/download/v0.18.0
        download "$restic_base/$restic_asset" "$TMP/restic.bz2"
        download "$restic_base/SHA256SUMS" "$TMP/restic.sums"
        awk -v file="$restic_asset" '$2 == file {print}' "$TMP/restic.sums" > "$TMP/restic.sha256"
        verify_file "$TMP/restic.bz2" "$TMP/restic.sha256"
        bzip2 -dc "$TMP/restic.bz2" > "$TMP/restic"
        chmod 755 "$TMP/restic"; "$TMP/restic" version >/dev/null
        install -m 755 "$TMP/restic" /usr/local/bin/restic
    fi
    install -d -m 700 /var/lib/vaultmesh-agent
    if [ "$ACTION" = upgrade-agent ]; then
        BACKUP=$(mktemp -d /var/lib/vaultmesh-agent/upgrade-backup.XXXXXX)
        cp -p /usr/local/bin/vaultmesh-agent "$BACKUP/vaultmesh-agent"
        cp -p /etc/vaultmesh-agent.env "$BACKUP/agent.env"
        cp -p /etc/systemd/system/vaultmesh-agent.service "$BACKUP/agent.service"
        AGENT_STOPPED=true
        systemctl stop vaultmesh-agent
        cp -p /var/lib/vaultmesh-agent/state.json "$BACKUP/state.json"
        printf 'Agent 升级前备份：%s（含设备凭据，不要复制到其他在线主机）\n' "$BACKUP"
    else
        install -m 644 "$TMP/agent.service" /etc/systemd/system/vaultmesh-agent.service
        printf 'VAULTMESH_SERVER_URL=%s\nVAULTMESH_ENROLLMENT_TOKEN=%s\n' "$AGENT_URL" "$AGENT_TOKEN" > /etc/vaultmesh-agent.env
        chmod 600 /etc/vaultmesh-agent.env
    fi
    install -m 755 "$TMP/agent" /usr/local/bin/vaultmesh-agent.new
    mv -f /usr/local/bin/vaultmesh-agent.new /usr/local/bin/vaultmesh-agent
    systemctl daemon-reload
    if [ "$ACTION" = install-agent ]; then systemctl enable vaultmesh-agent; fi
    systemctl start vaultmesh-agent
    attempt=0
    while [ "$attempt" -lt 40 ]; do
        if systemctl is-active --quiet vaultmesh-agent && grep -q '"agent_id"' /var/lib/vaultmesh-agent/state.json 2>/dev/null; then break; fi
        attempt=$((attempt + 1)); sleep 1
    done
    [ "$attempt" -lt 40 ] || fail "Agent 未就绪；保留身份与配置。检查 journalctl -u vaultmesh-agent；升级备份：$BACKUP"
    if [ "$ACTION" = install-agent ]; then
        sed '/^VAULTMESH_ENROLLMENT_TOKEN=/d' /etc/vaultmesh-agent.env > "$TMP/enrolled.env"
        install -m 600 "$TMP/enrolled.env" /etc/vaultmesh-agent.env
    fi
    AGENT_STOPPED=false
    printf 'Agent %s 已启动；请在控制台确认版本、心跳和依赖探测。\n' "$VERSION"
}

main() {
    ACTION=${1:-help}; [ "$#" -eq 0 ] || shift
    case "$ACTION" in help|--help|-h) help_text; return;; install|upgrade|status|logs|backup|compose|install-agent|upgrade-agent) ;; *) fail "未知命令：${ACTION}（运行 --help 查看用法）";; esac
    if [ "$ACTION" = install-agent ]; then
        [ "$#" -ge 2 ] || fail '用法：install-agent <url> <token> [--version vX.Y.Z]'
        AGENT_URL=$1; AGENT_TOKEN=$2; shift 2
    fi
    case "$ACTION" in *-agent) VERSION=${VAULTMESH_AGENT_VERSION:-$VERSION};; esac
    if [ "$ACTION" != compose ]; then
        while [ "$#" -gt 0 ]; do
            [ "$#" -ge 2 ] || fail "选项缺少值：$1"
            case "$1" in --version) VERSION=$2;; --domain) DOMAIN=$2;; --url) PUBLIC_URL=$2;; --proxy) MODE=$2;; --dir) INSTALL_DIR=$2;; *) fail "未知选项：$1";; esac
            shift 2
        done
    fi
    case "$MODE" in managed|external) ;; *) fail '--proxy 必须为 managed 或 external';; esac
    [ "$(uname -s)" = Linux ] || fail '一键安装仅支持 Linux；macOS 二进制仅供手动开发测试'
    [ "$(id -u)" -eq 0 ] || fail '请使用 sudo sh install.sh ... 或 root 运行'
    valid_line "$INSTALL_DIR" || fail '安装目录不能包含换行'
    printf '%s\n' "$INSTALL_DIR" | grep -Eq '^/(opt|srv|var/lib)/[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$' || fail '安装目录须是 /opt、/srv 或 /var/lib 下的独立子目录'
    [ "$(readlink -m "$INSTALL_DIR")" = "$INSTALL_DIR" ] || fail '安装路径不能包含符号链接'
    if [ "$ACTION" = compose ]; then need docker; load_installation; current_compose "$@"; return; fi
    need curl; need sha256sum; need tar
    case "$ACTION" in
        *-agent) lock_parent=/var/lib/vaultmesh-agent;;
        *) lock_parent=$INSTALL_DIR;;
    esac
    if [ ! -d "$lock_parent" ]; then install -d -m 700 "$lock_parent"; fi
    mkdir "$lock_parent/.operation-lock" 2>/dev/null || fail "已有维护操作或中断锁：$lock_parent/.operation-lock；确认无其他进程后才能移除"
    LOCK="$lock_parent/.operation-lock"
    trap finish EXIT
    trap 'exit 1' HUP INT TERM
    TMP=$(mktemp -d "${TMPDIR:-/tmp}/vaultmesh-install.XXXXXX")
    case "$ACTION" in *-agent) agent_action;; *) control_action;; esac
}

# The test harness sources helpers only, never invokes privileged main().
if [ "${VAULTMESH_INSTALLER_SOURCE_ONLY:-0}" != 1 ]; then main "$@"; fi
