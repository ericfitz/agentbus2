#!/usr/bin/env bash
# go vet and go build for every external key provider build tag, each alone and
# all four together, for linux and windows (signing-keys plan, Task 9). The
# adapter packages carry no tag, so the default gates already compile them; the
# tags add the registering files in internal/server/cmd and the SDKs they link.
#   usage: vet-tags.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

all=awskms,gcpkms,azurekms,hashivault
sets=(awskms gcpkms azurekms hashivault "$all")
for goos in linux windows; do
	for tags in "${sets[@]}"; do
		echo "vet-tags: GOOS=$goos -tags $tags"
		GOOS="$goos" CGO_ENABLED=0 go vet -tags "$tags" ./...
		GOOS="$goos" CGO_ENABLED=0 go build -tags "$tags" -o /dev/null .
	done
done
echo "vet-tags: ok"
