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
export VAULTMESH_HTTP_PORT=18330
export VAULTMESH_PUBLIC_API_URL=http://127.0.0.1:18330
export VAULTMESH_COOKIE_SECURE=false
smoke_tmp=$(mktemp -d "${TMPDIR:-/tmp}/vaultmesh-proxy-smoke.XXXXXX")
nginx_id=
compose() { docker compose -p vaultmesh-packaging-ci -f deploy/release/compose.yaml -f deploy/release/compose.external.yaml "$@" </dev/null; }
# Do not adopt an unrelated concurrent test's containers or database.
existing=$(docker ps -a --filter label=com.docker.compose.project=vaultmesh-packaging-ci --format '{{.ID}}')
[ -z "$existing" ] || { printf 'Smoke project is already in use.\n' >&2; exit 1; }
existing_volumes=$(docker volume ls --filter label=com.docker.compose.project=vaultmesh-packaging-ci --format '{{.Name}}')
[ -z "$existing_volumes" ] || { printf 'Smoke database/volumes already exist; refusing to reuse them.\n' >&2; exit 1; }
cleanup() {
    code=$?
    trap - EXIT HUP INT TERM
    if [ "$code" -ne 0 ]; then compose logs --tail 80 control gateway; fi
    if [ -n "$nginx_id" ]; then docker rm --force "$nginx_id" >/dev/null; fi
    compose down --volumes --remove-orphans
    rm -rf "$smoke_tmp"
    exit "$code"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
docker tag vaultmesh-control:ci ghcr.io/to-alan/vaultmesh/vaultmesh-control:packaging-ci
docker tag vaultmesh-web:ci ghcr.io/to-alan/vaultmesh/vaultmesh-web:packaging-ci
compose up -d --no-build --wait --wait-timeout 180
postgres_id=$(compose ps -q postgres)
curl -fSs http://127.0.0.1:18330/healthz
curl -fSs http://127.0.0.1:18330/config.txt | grep -F http://127.0.0.1:18330
curl -fSs http://127.0.0.1:18330/api/v1/meta | grep -E '"https_ready"[[:space:]]*:[[:space:]]*false'
curl -fSs --cookie-jar "$smoke_tmp/cookie" --dump-header "$smoke_tmp/http-headers" -H 'Content-Type: application/json' \
    -H 'Origin: http://127.0.0.1:18330' \
    --data '{"username":"admin","password":"vaultmesh-ci-administrator-password"}' \
    http://127.0.0.1:18330/api/v1/auth/login
if grep -Ei '^set-cookie:.*;[[:space:]]*secure' "$smoke_tmp/http-headers"; then exit 1; fi
curl -fSs --cookie "$smoke_tmp/cookie" http://127.0.0.1:18330/api/v1/dashboard
code=$(curl -sS --cookie "$smoke_tmp/cookie" --output "$smoke_tmp/gate" --write-out '%{http_code}' \
    --request POST http://127.0.0.1:18330/api/v1/servers/fixture/detect)
[ "$code" = 403 ]
grep -F https_required "$smoke_tmp/gate"

# Adopt an existing HTTPS proxy later; never publish VaultMesh itself on 80/443.
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -keyout "$smoke_tmp/server.key" -out "$smoke_tmp/server.crt" \
    -subj /CN=localhost -addext 'subjectAltName=DNS:localhost' >/dev/null 2>&1
export VAULTMESH_PUBLIC_API_URL=https://localhost:18445
export VAULTMESH_COOKIE_SECURE=true
compose up -d --no-deps --no-build --pull never --wait --wait-timeout 180 control web gateway
[ "$(compose ps -q postgres)" = "$postgres_id" ]
nginx_id=$(docker run --detach --network vaultmesh-packaging-ci_default \
    --publish 127.0.0.1:18445:443 \
    --volume "$PWD/scripts/tests/nginx.external.conf:/etc/nginx/conf.d/default.conf:ro" \
    --volume "$smoke_tmp/server.key:/etc/nginx/test-tls/server.key:ro" \
    --volume "$smoke_tmp/server.crt:/etc/nginx/test-tls/server.crt:ro" vaultmesh-web:ci)
docker exec "$nginx_id" nginx -t
curl -fSs --cacert "$smoke_tmp/server.crt" --retry 10 --retry-connrefused --retry-delay 1 https://localhost:18445/healthz
curl -fSs --cacert "$smoke_tmp/server.crt" https://localhost:18445/config.txt | grep -F https://localhost:18445
curl -fSs --cacert "$smoke_tmp/server.crt" https://localhost:18445/api/v1/meta | grep -E '"https_ready"[[:space:]]*:[[:space:]]*true'
curl -fSs --cacert "$smoke_tmp/server.crt" --cookie-jar "$smoke_tmp/https-cookie" --dump-header "$smoke_tmp/https-headers" \
    -H 'Content-Type: application/json' -H 'Origin: https://localhost:18445' \
    --data '{"username":"admin","password":"vaultmesh-ci-administrator-password"}' https://localhost:18445/api/v1/auth/login
grep -Ei '^set-cookie:.*;[[:space:]]*secure' "$smoke_tmp/https-headers" >/dev/null
curl -fSs --cacert "$smoke_tmp/server.crt" --cookie "$smoke_tmp/https-cookie" https://localhost:18445/api/v1/dashboard
compose exec -T postgres pg_dump -U vaultmesh -d vaultmesh --format=custom > "$smoke_tmp/postgres.dump"
test -s "$smoke_tmp/postgres.dump"
printf '\nLoopback bootstrap, external Nginx HTTPS, login, and preserved PostgreSQL smoke passed.\n'
