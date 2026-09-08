#!/bin/sh
# Push already committed work; never auto-stage files, force-push or create tags.
set -eu
cd "$(dirname "$0")/.."
[ -z "$(git status --porcelain)" ] || { printf '请先审查并提交改动（不自动 git add .，避免误提交密钥）。\n' >&2; exit 1; }
branch=$(git symbolic-ref --quiet --short HEAD) || { printf '请切换到分支后推送。\n' >&2; exit 1; }
git fetch origin
if git show-ref --verify --quiet "refs/remotes/origin/$branch"; then
    git merge-base --is-ancestor "origin/$branch" HEAD || { printf '远端有未合并提交；请先合并/变基，不会强制覆盖。\n' >&2; exit 1; }
fi
git diff --check
git push --set-upstream origin "$branch"
printf '已推送 %s。main/PR 会触发 CI；本操作不发布正式版本。\n' "$branch"
