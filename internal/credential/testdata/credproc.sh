#!/usr/bin/env bash
# The fake credential_process of the credential tests. CREDPROC_MODE selects
# the output; CREDPROC_CAPTURE names a file that receives the stdin JSON;
# CREDPROC_PIDFILE a file that receives this process's pid; CREDPROC_COUNT a
# file to which one line is appended per run. The values come from the
# environment: CREDPROC_PEM, CREDPROC_ASSERTION, CREDPROC_TOKEN and
# CREDPROC_EXPIRES.
set -euo pipefail

input="$(cat)"
if [ -n "${CREDPROC_CAPTURE:-}" ]; then printf '%s' "$input" >"$CREDPROC_CAPTURE"; fi
if [ -n "${CREDPROC_PIDFILE:-}" ]; then printf '%s' "$$" >"$CREDPROC_PIDFILE"; fi
if [ -n "${CREDPROC_COUNT:-}" ]; then echo run >>"$CREDPROC_COUNT"; fi

pem="${CREDPROC_PEM:-}"
pem="${pem//$'\n'/\\n}"

case "${CREDPROC_MODE:?}" in
pem) printf '{"version":1,"private_key_pem":"%s"}\n' "$pem" ;;
assertion) printf '{"version":1,"client_assertion":"%s"}\n' "${CREDPROC_ASSERTION:-}" ;;
token) printf '{"version":1,"access_token":"%s","expires_at":"%s"}\n' "${CREDPROC_TOKEN:-}" "${CREDPROC_EXPIRES:-}" ;;
token-no-expiry) printf '{"version":1,"access_token":"x"}\n' ;;
two) printf '{"version":1,"client_assertion":"a","access_token":"b","expires_at":"2099-01-01T00:00:00Z"}\n' ;;
none) printf '{"version":1}\n' ;;
garbage) echo 'not json' ;;
version2) printf '{"version":2,"client_assertion":"a"}\n' ;;
unknown) printf '{"version":1,"client_assertion":"a","extra":1}\n' ;;
casefold) printf '{"version":1,"Client_Assertion":"a"}\n' ;;
dup) printf '{"version":1,"client_assertion":"a","client_assertion":"b"}\n' ;;
trailing) printf '{"version":1,"client_assertion":"a"}{}\n' ;;
big) head -c 70000 /dev/zero | tr '\0' 'a'; echo ;;
exit1)
	echo "boom: vault unreachable" >&2
	exit 1
	;;
hang) exec sleep 30 ;;
*)
	echo "unknown mode" >&2
	exit 9
	;;
esac
