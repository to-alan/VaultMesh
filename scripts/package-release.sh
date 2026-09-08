#!/bin/sh
# Build a small deployment kit; application images are pulled from GHCR.
set -eu
repo_dir=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
export VAULTMESH_INSTALLER_SOURCE_ONLY=1
. "$repo_dir/install.sh"
package_version=${1:?usage: package-release.sh vX.Y.Z [output-directory]}
valid_version "$package_version" || fail '无效版本号'
output_dir=${2:-$repo_dir/dist}
mkdir -p "$output_dir"
output_dir=$(CDPATH='' cd -- "$output_dir" && pwd)
package_tmp=$(mktemp -d "${TMPDIR:-/tmp}/vaultmesh-package.XXXXXX")
trap 'rm -rf "$package_tmp"' EXIT
trap 'exit 1' HUP INT TERM
mkdir -p "$package_tmp/deploy/systemd" "$package_tmp/docs"
cp "$repo_dir/install.sh" "$package_tmp/install.sh"
chmod 755 "$package_tmp/install.sh"
cp "$repo_dir"/deploy/release/* "$package_tmp/"
cp "$repo_dir"/deploy/systemd/* "$package_tmp/deploy/systemd/"
cp "$repo_dir/LICENSE" "$repo_dir/README.md" "$repo_dir/CHANGELOG.md" "$repo_dir/CONTRIBUTING.md" "$repo_dir/SECURITY.md" "$repo_dir/VaultMesh-项目说明书.md" "$package_tmp/"
cp "$repo_dir"/docs/*.md "$package_tmp/docs/"
cp "$repo_dir/docs/openapi.yaml" "$package_tmp/docs/"
printf '%s\n' "$package_version" > "$package_tmp/VERSION"
git -C "$repo_dir" rev-parse HEAD > "$package_tmp/COMMIT"
tar -czf "$output_dir/vaultmesh-deploy-$package_version.tar.gz" -C "$package_tmp" .
cp "$package_tmp/install.sh" "$output_dir/install.sh"
cp "$repo_dir/LICENSE" "$output_dir/LICENSE"
cp "$package_tmp/VERSION" "$package_tmp/COMMIT" "$output_dir/"
cd "$output_dir"
for asset in "vaultmesh-deploy-$package_version.tar.gz" install.sh; do
    shasum -a 256 "$asset" > "$asset.sha256"
done
printf '部署包已生成：%s/vaultmesh-deploy-%s.tar.gz\n' "$output_dir" "$package_version"
