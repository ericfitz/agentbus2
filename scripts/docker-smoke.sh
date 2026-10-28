#!/usr/bin/env bash
# Builds the Docker image and proves its claims: the exit codes of a bad configuration, the version, the non-root
# user, a serving container, the bootstrap path inside the image. Not part of make verify (make docker-smoke).
# Containers are named, removed first and removed again by the trap, so a killed run leaves at most those three.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME="${DOCKER_SMOKE_NAME:-agentbus2-smoke}"
IMAGE="agentbus2:smoke"
cleanup() { docker rm -f -v "$NAME" "$NAME-nocfg" "$NAME-nolisten" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

fail() { echo "docker-smoke: FAIL: $*" >&2; exit 1; }

# expect_exit NAME CODE TEXT ARGS...: run detached (no --rm: an exited --rm container cannot be inspected), wait up to
# 10 s for it to exit, then check the exit code and that its log contains TEXT.
expect_exit() {
  local name="$1" code="$2" text="$3"
  shift 3
  docker run -d --name "$name" "$@" >/dev/null
  local status=""
  for _ in $(seq 1 10); do
    sleep 1
    status="$(docker inspect --format '{{.State.Status}}' "$name")"
    if [ "$status" = exited ]; then break; fi
  done
  if [ "$status" != exited ]; then
    docker rm -f -v "$name" >/dev/null 2>&1 || true
    fail "$name did not exit within 10 s"
  fi
  local got logs
  got="$(docker inspect --format '{{.State.ExitCode}}' "$name")"
  logs="$(docker logs "$name" 2>&1)"
  docker rm -f -v "$name" >/dev/null
  [ "$got" = "$code" ] || fail "$name exited $got, want $code; log: $logs"
  case "$logs" in
    *"$text"*) ;;
    *) fail "$name log lacks \"$text\"; log: $logs" ;;
  esac
}

echo "step 1: docker build"
docker build -q -t "$IMAGE" --build-arg VERSION=smoke "$ROOT" >/dev/null

echo "step 2: no configuration exits 2"
expect_exit "$NAME-nocfg" 2 'exactly one of sqlite or postgres is required' "$IMAGE"
echo "step 2b: a store without listen exits 2"
expect_exit "$NAME-nolisten" 2 'listen is required' -e AGENTBUS2_SERVER_SQLITE=/data/agentbus2.db "$IMAGE"

echo "step 2c: every key provider backend is in the image"
# The image carries all four backends (BUILD_TAGS in the Dockerfile; open note signing-keys 1); an untagged binary
# answers every scheme with "not in this build". With the placeholder URI "<scheme>://none" the three URI-checking
# backends refuse it as a configuration error (exit 2) and hashivault, which checks its environment (VAULT_ADDR) at
# EnsureActive, fails as a runtime error (exit 1). No key service is needed. The log must carry the backend's own text
# and must not quote the URI.
for spec in awskms:2:'agentbus2 server: awskms:' gcpkms:2:'agentbus2 server: gcpkms:' azurekms:2:'agentbus2 server: azurekms:' \
  hashivault:1:'signing key provider hashivault'; do
  scheme="${spec%%:*}"
  rest="${spec#*:}"
  want="${rest%%:*}"
  text="${rest#*:}"
  expect_exit "$NAME-nolisten" "$want" "$text" -e AGENTBUS2_SERVER_LISTEN=0.0.0.0:8420 \
    -e AGENTBUS2_SERVER_SQLITE=/data/agentbus2.db -e "AGENTBUS2_SERVER_SIGNING_KEY_URI=$scheme://none" "$IMAGE"
done

echo "step 3: version"
got="$(docker run --rm --entrypoint /agentbus2 "$IMAGE" version)"
[ "$got" = smoke ] || fail "version printed \"$got\", want smoke"

echo "step 4: non-root user"
got="$(docker inspect --format '{{.Config.User}}' "$IMAGE")"
[ "$got" = nonroot ] || fail "image user is \"$got\", want nonroot"

echo "step 5: serving container"
docker run -d --name "$NAME" -p 127.0.0.1::8420 \
  -e AGENTBUS2_SERVER_LISTEN=0.0.0.0:8420 -e AGENTBUS2_SERVER_SQLITE=/data/agentbus2.db \
  "$IMAGE" -registration open >/dev/null
port="$(docker port "$NAME" 8420/tcp | head -n 1 | sed 's/.*://')"
[ -n "$port" ] || fail "no published port for $NAME"
body=""
for _ in $(seq 1 20); do
  if body="$(curl -fsS "http://127.0.0.1:$port/v1/discover" 2>/dev/null)"; then break; fi
  body=""
  sleep 1
done
[ -n "$body" ] || fail "/v1/discover did not answer within 20 s; log: $(docker logs "$NAME" 2>&1)"
case "$body" in
  *'"long_poll"'*) ;;
  *) fail "/v1/discover lacks \"long_poll\": $body" ;;
esac

echo "step 5b: GET /v1/health answers the probe tier"
hbody="$(curl -fsS "http://127.0.0.1:$port/v1/health")" || fail "/v1/health did not answer 200; log: $(docker logs "$NAME" 2>&1)"
case "$hbody" in
  *'"tier":"probe"'*) ;;
  *) fail "/v1/health lacks \"tier\":\"probe\": $hbody" ;;
esac
case "$hbody" in
  *'"status":"ok"'*) ;;
  *) fail "/v1/health lacks \"status\":\"ok\": $hbody" ;;
esac

echo "step 5c: a rejected bearer on /v1/health is a 401"
code="$(curl -sS -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer bad' "http://127.0.0.1:$port/v1/health")"
[ "$code" = 401 ] || fail "/v1/health with a bad bearer: want 401, got $code"

echo "step 6: register with an empty body is a 400 problem"
hdr="$(curl -sS -o /dev/null -D - -X POST "http://127.0.0.1:$port/v1/auth/register" | tr -d '\r')"
case "$hdr" in
  "HTTP/"*" 400"*) ;;
  *) fail "register with an empty body: want 400, got: $hdr" ;;
esac
case "$(printf '%s' "$hdr" | tr '[:upper:]' '[:lower:]')" in
  *'content-type: application/problem+json'*) ;;
  *) fail "register with an empty body: not problem+json: $hdr" ;;
esac

echo "step 7: bootstrap inside the image"
got="$(docker exec "$NAME" /agentbus2 server admin -sqlite /data/agentbus2.db create-identity pat 2>&1)"
case "$got" in *'pat created'*) ;; *) fail "create-identity printed: $got" ;; esac
got="$(docker exec "$NAME" /agentbus2 server admin -sqlite /data/agentbus2.db admins add -identity pat 2>&1)"
case "$got" in *added*) ;; *) fail "admins add printed: $got" ;; esac

echo "step 8: startup log"
logs="$(docker logs "$NAME" 2>&1)"
case "$logs" in
  # the server prints the resolved listener address ([::]:8420 for 0.0.0.0:8420), so match the port and the mode
  *':8420 (sqlite, registration open'*) ;;
  *) fail "log lacks the listening line with 'registration open': $logs" ;;
esac
case "$logs" in *'generated signing key'*) ;; *) fail "log lacks 'generated signing key': $logs" ;; esac

echo "step 9: compose example validates; its .env is neither committed nor in the build context"
git -C "$ROOT" check-ignore -q deploy/.env || fail "deploy/.env is not git-ignored"
grep -qxF '**/.env' "$ROOT/.dockerignore" || fail ".dockerignore does not exclude **/.env"
docker compose -f "$ROOT/deploy/docker-compose.yml" --env-file "$ROOT/deploy/example.env" config >/dev/null

echo "docker-smoke: ok"
