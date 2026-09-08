#!/bin/sh
# Explicit maintainer action only. The tag-triggered workflow re-runs all CI.
set -eu
cd "$(dirname "$0")/.."
export VAULTMESH_INSTALLER_SOURCE_ONLY=1
. ./install.sh
tag=${1:?usage: make release VERSION=vX.Y.Z}
valid_version "$tag" || fail '只支持 vX.Y.Z 或 vX.Y.Z-rc.N'
[ "$(git branch --show-current)" = main ] || fail '只允许从 main 发布'
[ -z "$(git status --porcelain)" ] || fail '请先提交所有改动'
git fetch origin --tags
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || fail '本地 main 必须与 GitHub main 完全一致；先 make push'
if git rev-parse --verify "refs/tags/$tag" >/dev/null 2>&1; then fail '标签已存在；不要移动或覆盖已发布版本'; fi
git tag -a "$tag" -m "VaultMesh $tag"
git push origin "refs/tags/$tag"
printf '已推送发布标签 %s；请在 GitHub Actions 的 Release 中确认成功，成功前不可通知用户升级。\n' "$tag"
