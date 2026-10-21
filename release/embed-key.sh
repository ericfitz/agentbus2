#!/usr/bin/env bash
# Embed a PEM public key into install scripts, between the markers
#   # BEGIN agentbus2 release public key
#   # END agentbus2 release public key
# A .sh script gets  PUBKEY_PEM='<pem>'  and a .ps1 script gets
#   $PubKeyPem = @'
#   <pem>
#   '@
# Idempotent: running it again with the same key changes nothing.
#   release/embed-key.sh release/agentbus2-release-ed25519.pub install.sh install.ps1
set -euo pipefail

BEGIN='# BEGIN agentbus2 release public key'
END='# END agentbus2 release public key'

[[ $# -ge 2 ]] || { echo "usage: embed-key.sh <pubkey.pem> <script>..." >&2; exit 2; }
PUB="$1"; shift
[[ -r "$PUB" ]] || { echo "error: no such public key: $PUB" >&2; exit 1; }
if ! { grep -q '^-----BEGIN PUBLIC KEY-----$' "$PUB" && grep -q '^-----END PUBLIC KEY-----$' "$PUB"; }; then
    echo "error: $PUB is not a PEM public key" >&2; exit 1
fi
PEM="$(cat "$PUB")"

for script in "$@"; do
    [[ -f "$script" ]] || { echo "error: no such script: $script" >&2; exit 1; }
    if ! { grep -qxF "$BEGIN" "$script" && grep -qxF "$END" "$script"; }; then
        echo "error: $script has no '$BEGIN' / '$END' markers" >&2; exit 1
    fi
    case "$script" in
        *.ps1) block="\$PubKeyPem = @'"$'\n'"$PEM"$'\n'"'@" ;;
        *)     block="PUBKEY_PEM='$PEM'" ;;
    esac
    # The temp file goes next to the script, so the final mv is a rename on
    # one filesystem, and it gets the script's exact mode back before the mv
    # (a read-only script stays read-only).
    mode="$(stat -c %a "$script" 2>/dev/null || stat -f %Lp "$script")"
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] || { echo "error: could not read the mode of $script (got '$mode')" >&2; exit 1; }
    tmp="$(mktemp "$(dirname -- "$script")/.embed-key.XXXXXX")"
    trap 'rm -f "$tmp"' EXIT
    # The block goes through the environment, not -v: awk -v would interpret
    # backslashes, and ENVIRON keeps the embedded newlines verbatim.
    # A BEGIN with no later END (END before BEGIN) would swallow the rest of
    # the file, so awk exits 1 in that case.
    if ! BLOCK="$block" awk -v begin="$BEGIN" -v end="$END" '
        $0 == begin { print; print ENVIRON["BLOCK"]; skipping = 1; next }
        $0 == end   { skipping = 0 }
        !skipping   { print }
        END         { if (skipping) exit 1 }
    ' "$script" > "$tmp"; then
        echo "error: $script: rewrite failed (is '$END' after '$BEGIN'?)" >&2; exit 1
    fi
    if cmp -s "$tmp" "$script"; then rm -f "$tmp"; echo "$script: unchanged"; else
        chmod "$mode" "$tmp"
        mv "$tmp" "$script"; echo "$script: key embedded"
    fi
done
