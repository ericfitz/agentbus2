#!/usr/bin/env bash
# ci-gate-negative.sh <expected-text>: make verify must FAIL, its log must contain <expected-text> and must not contain "verify: OK".
# Exit 0 when the gate failed for the expected reason; exit 1 otherwise. The caller sets (or unsets) AGENTBUS2_TEST_POSTGRES_DSN.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
expected="${1:?usage: ci-gate-negative.sh <expected-text>}"
log="$(mktemp)"
trap 'rm -f "$log"' EXIT
if make -C "$ROOT" verify 2>&1 | tee "$log"; then
  echo "gate-negative: FAIL: make verify passed"; exit 1
fi
grep -qF -- "$expected" "$log" || { echo "gate-negative: FAIL: log lacks the expected text: $expected"; exit 1; }
if grep -qF 'verify: OK' "$log"; then echo "gate-negative: FAIL: verify: OK printed on a failed run"; exit 1; fi
echo "gate-negative: ok ($expected)"
