#!/usr/bin/env bash
# Simulates ci.yml's verify job locally: Chainguard Postgres 18 on host port 5433, then make verify through AGENTBUS2_TEST_POSTGRES_DSN.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAME="${CI_SIM_NAME:-agentbus2-ci-sim}"
PORT="${CI_SIM_PORT:-5433}"
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -e POSTGRES_USER=agentbus2 -e POSTGRES_PASSWORD=agentbus2 -e POSTGRES_DB=agentbus2 -p "$PORT:5432" cgr.dev/chainguard/postgres:latest@sha256:0926ac7262a2c1963179eb939cd5d610e1be3ecca90b4b5cad8db8df16fab87e >/dev/null
# The image restarts postgres once during first-boot init, so pg_isready can answer before the final start: require two consecutive answers.
ready=0
for _ in $(seq 1 60); do
  if docker exec "$NAME" pg_isready -U agentbus2 >/dev/null 2>&1; then ready=$((ready + 1)); else ready=0; fi
  if [ "$ready" -ge 2 ]; then break; fi
  sleep 1
done
if [ "$ready" -lt 2 ]; then echo "ci-sim: postgres not ready within 60 s"; exit 1; fi
AGENTBUS2_TEST_POSTGRES_DSN="postgres://agentbus2:agentbus2@127.0.0.1:$PORT/agentbus2?sslmode=disable" make -C "$ROOT" verify
