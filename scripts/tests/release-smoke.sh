#!/bin/sh
# Real PostgreSQL + release gateway smoke test; all resources have a dedicated name.
set -eu
cd "$(dirname "$0")/../.."
export POSTGRES_PASSWORD=vaultmesh_ci_postgres
export VAULTMESH_ADMIN_USERNAME=admin
export VAULTMESH_ADMIN_PASSWORD=vaultmesh-ci-administrator-password
export VAULTMESH_MASTER_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
export VAULTMESH_IMAGE_TAG=packaging-ci
export VAULTMESH_RELEASE_DIR="$PWD/deploy/release"
export VAULTMESH_SITE_ADDRESS=http://:80
export VAULTMESH_PUBLIC_API_URL=https://backup.example.test
smoke_tmp=$(mktemp -d "${TMPDIR:-/tmp}/vaultmesh-proxy-smoke.XXXXXX")
compose() { docker compose -p vaultmesh-packaging-ci -f deploy/release/compose.yaml -f scripts/tests/compose.smoke.yaml "$@" </dev/null; }
cleanup() {
    code=$?
    trap - EXIT HUP INT TERM
    if [ "$code" -ne 0 ]; then compose logs --tail 80 control gateway; fi
    compose down --volumes --remove-orphans
    rm -rf "$smoke_tmp"
    exit "$code"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
docker tag vaultmesh-control:ci ghcr.io/to-alan/vaultmesh/vaultmesh-control:packaging-ci
docker tag vaultmesh-web:ci ghcr.io/to-alan/vaultmesh/vaultmesh-web:packaging-ci
compose up -d --no-build --wait --wait-timeout 180
curl -fSs http://localhost:18330/healthz
curl -fSs http://localhost:18330/config.txt | grep -F https://backup.example.test
curl -fSs http://localhost:18330/api/v1/meta | grep -E '"https_ready"[[:space:]]*:[[:space:]]*true'
curl -fSs --cookie-jar "$smoke_tmp/cookie" -H 'Content-Type: application/json' \
    -H 'Origin: https://backup.example.test' \
    --data '{"username":"admin","password":"vaultmesh-ci-administrator-password"}' \
    http://localhost:18330/api/v1/auth/login
# Curl treats localhost as a secure cookie context; external users still need HTTPS.
curl -fSs --cookie "$smoke_tmp/cookie" http://localhost:18330/api/v1/dashboard
compose exec -T postgres pg_dump -U vaultmesh -d vaultmesh --format=custom > "$smoke_tmp/postgres.dump"
test -s "$smoke_tmp/postgres.dump"
printf '\nRelease proxy/login/PostgreSQL smoke passed.\n'
