#!/usr/bin/env bash
# Builds the Docker image and proves its claims: the exit codes of a bad configuration, the version, the non-root
# user, a serving container, the bootstrap path inside the image. Not part of make verify (make docker-smoke).
# Containers are named, removed first and removed again by the trap, so a killed run leaves at most those three.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME="${DOCKER_SMOKE_NAME:-agentbus2-smoke}"
IMAGE="agentbus2:smoke"
cleanup() { docker rm -f "$NAME" "$NAME-nocfg" "$NAME-nolisten" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

fail() { echo "docker-smoke: FAIL: $*" >&2; exit 1; }

# expect_exit NAME CODE TEXT ARGS...: run detached (no --rm: an exited --rm container cannot be inspected), wait up to
# 10 s for it to exit, then check the exit code and that its log contains TEXT.
expect_exit() {
  local name="$1" code="$2" text="$3"
  shift 3
  docker run -d --name "$name" "$@" >/dev/null
  local status="" i
  for i in $(seq 1 10); do
    sleep 1
    status="$(docker inspect --format '{{.State.Status}}' "$name")"
    if [ "$status" = exited ]; then break; fi
  done
  if [ "$status" != exited ]; then
    docker rm -f "$name" >/dev/null 2>&1 || true
    fail "$name did not exit within 10 s"
  fi
  local got logs
  got="$(docker inspect --format '{{.State.ExitCode}}' "$name")"
  logs="$(docker logs "$name" 2>&1)"
  docker rm -f "$name" >/dev/null
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

echo "step 3: version"
got="$(docker run --rm --entrypoint /agentbus2 "$IMAGE" version)"
[ "$got" = smoke ] || fail "version printed \"$got\", want smoke"

echo "step 4: non-root user"
got="$(docker inspect --format '{{.Config.User}}' "$IMAGE")"
[ "$got" = nonroot ] || fail "image user is \"$got\", want nonroot"

echo "step 5: serving container"
docker run -d --name "$NAME" -p 127.0.0.1::8420 \
  -e AGENTBUS2_SERVER_LISTEN=0.0.0.0:8420 -e AGENTBUS2_SERVER_SQLITE=/data/agentbus2.db \
  "$IMAGE" -registration closed >/dev/null
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

echo "step 6: register with an empty body is a 400 problem"
hdr="$(curl -sS -o /dev/null -D - -X POST "http://127.0.0.1:$port/v1/auth/register" | tr -d '\r')"
case "$hdr" in
  "HTTP/"*" 400"*) ;;
  *) fail "register with an empty body: want 400, got: $hdr" ;;
esac
case "$(printf '%s' "$hdr" | tr 'A-Z' 'a-z')" in
  *'content-type: application/problem+json'*) ;;
  *) fail "register with an empty body: not problem+json: $hdr" ;;
esac

echo "step 7: bootstrap inside the image"
got="$(docker exec "$NAME" /agentbus2 server admin -sqlite /data/agentbus2.db create-identity pat 2>&1)"
case "$got" in *'pat created'*) ;; *) fail "create-identity printed: $got" ;; esac
got="$(docker exec "$NAME" /agentbus2 server admin -sqlite /data/agentbus2.db make-admin pat 2>&1)"
case "$got" in *'pat is now an administrator'*) ;; *) fail "make-admin printed: $got" ;; esac

echo "step 8: startup log"
logs="$(docker logs "$NAME" 2>&1)"
case "$logs" in
  # the server prints the resolved listener address ([::]:8420 for 0.0.0.0:8420), so match the port and the mode
  *':8420 (sqlite, registration closed'*) ;;
  *) fail "log lacks the listening line with 'registration closed': $logs" ;;
esac
case "$logs" in *'generated signing key'*) ;; *) fail "log lacks 'generated signing key': $logs" ;; esac

echo "step 9: compose example validates"
docker compose -f "$ROOT/deploy/docker-compose.yml" --env-file "$ROOT/deploy/example.env" config >/dev/null

echo "docker-smoke: ok"
