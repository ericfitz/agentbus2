#!/usr/bin/env bash
# Builds one agentbus2 release binary for a GOOS/GOARCH pair and, for linux,
# its tar.gz. The one build step shared by release-build.yml and the local
# dry run (release/test-pipeline.sh). Ported from the inline build and
# package steps of v1 .github/workflows/release-build.yml.
#
#   build-archive.sh <tag> <goos> <goarch> <outdir>
#   build-archive.sh --self-test
#
# Run it from the module root. Windows and darwin leave the bare binary in
# <outdir>: the zip is packaged after signing (package-windows) and lipo runs
# in release.sh.
set -euo pipefail

USAGE='usage: build-archive.sh <tag> <goos> <goarch> <outdir> | --self-test'
MODULE_VERSION_VAR='github.com/ericfitz/agentbus2/internal/version.Version'

usage_fail() {
  echo "$USAGE" >&2
  exit 2
}

build() {
  local tag="$1" goos="$2" goarch="$3" outdir="$4" version ext="" bin got hostos hostarch
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    echo "build-archive.sh: tag must look like vX.Y.Z, got '$tag'" >&2
    exit 2
  }
  case "$goos/$goarch" in
    linux/amd64 | linux/arm64 | windows/amd64 | windows/arm64 | darwin/amd64 | darwin/arm64) ;;
    *)
      echo "build-archive.sh: unsupported pair $goos/$goarch (goos: linux, windows, darwin; goarch: amd64, arm64)" >&2
      exit 2
      ;;
  esac
  version="${tag#v}"
  [[ "$goos" == windows ]] && ext=".exe"
  mkdir -p "$outdir"
  bin="$outdir/agentbus2$ext"

  # open note: signing-keys 1: release archives are built without a key-provider tag (lean)
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags "-s -w -X ${MODULE_VERSION_VAR}=${version}" -o "$bin" .

  hostos="$(go env GOHOSTOS)"
  hostarch="$(go env GOHOSTARCH)"
  if [[ "$hostos" == "$goos" && "$hostarch" == "$goarch" ]]; then
    got="$("$bin" version)"
    [[ "$got" == "$version" ]] || {
      echo "version mismatch: $got != $version" >&2
      exit 1
    }
    echo "ok: $bin prints $got"
  else
    echo "note: not run (host is $hostos/$hostarch)"
  fi

  if [[ "$goos" == linux ]]; then
    tar -C "$outdir" -czf "$outdir/agentbus2-${tag}-linux-${goarch}.tar.gz" agentbus2
  fi
}

# --- self-test --------------------------------------------------------------

TMP_DIR=
SELF_PASS=0
SELF_FAIL=0

# expect_refusal <case> <want-substring> <args...>: exit 2 with the substring on stderr.
expect_refusal() {
  local name="$1" want="$2" rc=0 err
  shift 2
  err="$(bash "$SELF" "$@" 2>&1 >/dev/null)" || rc=$?
  if [[ "$rc" == 2 && "$err" == *"$want"* ]]; then
    echo "PASS $name"
    SELF_PASS=$((SELF_PASS + 1))
  else
    echo "FAIL $name: exit $rc, stderr: $err"
    SELF_FAIL=$((SELF_FAIL + 1))
  fi
}

self_test() {
  local hostos hostarch ext="" rc=0 out listing
  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_DIR"' EXIT
  local tmp="$TMP_DIR"
  SELF="${BASH_SOURCE[0]}"
  [[ "$SELF" == /* ]] || SELF="$PWD/$SELF"

  expect_refusal usage "$USAGE"
  expect_refusal too-many-args "$USAGE" v0.0.1 linux amd64 "$tmp" extra
  expect_refusal bad-flag "$USAGE" --bogus
  expect_refusal flag-in-position "$USAGE" v0.0.1 -linux amd64 "$tmp"
  expect_refusal bad-tag "tag must look like vX.Y.Z" v1.2 linux amd64 "$tmp"
  expect_refusal bad-tag-prerelease "tag must look like vX.Y.Z" v1.2.3-rc1 linux amd64 "$tmp"
  expect_refusal bad-pair "plan9/amd64" v0.0.1 plan9 amd64 "$tmp"
  expect_refusal bad-arch "linux/386" v0.0.1 linux 386 "$tmp"

  hostos="$(go env GOHOSTOS)"
  hostarch="$(go env GOHOSTARCH)"
  [[ "$hostos" == windows ]] && ext=".exe"
  case "$hostos/$hostarch" in
    linux/amd64 | linux/arm64 | windows/amd64 | windows/arm64 | darwin/amd64 | darwin/arm64)
      out="$(bash "$SELF" v0.0.7 "$hostos" "$hostarch" "$tmp/native" 2>&1)" || rc=$?
      if [[ "$rc" == 0 && "$(
        "$tmp/native/agentbus2$ext" version
      )" == "0.0.7" ]]; then
        echo "PASS native-build"
        SELF_PASS=$((SELF_PASS + 1))
      else
        echo "FAIL native-build: exit $rc: $out"
        SELF_FAIL=$((SELF_FAIL + 1))
      fi
      ;;
    *)
      echo "FAIL native-build: unsupported host $hostos/$hostarch"
      SELF_FAIL=$((SELF_FAIL + 1))
      ;;
  esac

  # The archive binary is lean: no key-provider tag, so no sigstore module (and no cloud SDK behind one) is linked.
  if [[ -f "$tmp/native/agentbus2$ext" ]]; then
    local mods
    if mods="$(go version -m "$tmp/native/agentbus2$ext" 2>&1)" && [[ "$mods" == *"agentbus2"* ]] \
      && [[ "$mods" != *sigstore* ]]; then
      echo "PASS archive-binary-links-no-sigstore"
      SELF_PASS=$((SELF_PASS + 1))
    else
      echo "FAIL archive-binary-links-no-sigstore: $(printf '%s' "$mods" | grep -i sigstore | head -3)"
      SELF_FAIL=$((SELF_FAIL + 1))
    fi
  else
    echo "FAIL archive-binary-links-no-sigstore: no native binary at $tmp/native/agentbus2$ext"
    SELF_FAIL=$((SELF_FAIL + 1))
  fi

  # The linux tarball on any host: the native one on Linux, else a cross
  # build (not run here, so only the packaging is judged).
  local tarball tardir="$tmp/native" tararch="$hostarch"
  if [[ "$hostos" != linux ]]; then
    tardir="$tmp/linux"
    tararch=amd64
    rc=0
    out="$(bash "$SELF" v0.0.7 linux amd64 "$tardir" 2>&1)" || rc=$?
    if [[ "$rc" == 0 && "$out" == *"note: not run (host is $hostos/$hostarch)"* ]]; then
      echo "PASS linux-cross-build"
      SELF_PASS=$((SELF_PASS + 1))
    else
      echo "FAIL linux-cross-build: exit $rc: $out"
      SELF_FAIL=$((SELF_FAIL + 1))
    fi
  fi
  tarball="$tardir/agentbus2-v0.0.7-linux-${tararch}.tar.gz"
  listing="$(tar -tzf "$tarball" 2>&1)" || true
  if [[ "$listing" == "agentbus2" ]]; then
    echo "PASS linux-tar-lists-binary"
    SELF_PASS=$((SELF_PASS + 1))
  else
    echo "FAIL linux-tar-lists-binary: $listing"
    SELF_FAIL=$((SELF_FAIL + 1))
  fi
  # The packed binary is the one built, and executable.
  if [[ -f "$tarball" ]] && tar -xzOf "$tarball" agentbus2 | cmp -s - "$tardir/agentbus2" \
    && [[ "$(tar -tvzf "$tarball" | cut -c1-10)" == -rwx* ]]; then
    echo "PASS linux-tar-holds-the-binary"
    SELF_PASS=$((SELF_PASS + 1))
  else
    echo "FAIL linux-tar-holds-the-binary: $tarball"
    SELF_FAIL=$((SELF_FAIL + 1))
  fi

  if ((SELF_FAIL > 0)); then
    echo "build-archive self-test: $SELF_FAIL failed, $SELF_PASS passed" >&2
    exit 1
  fi
  echo "build-archive self-test: OK"
}

# --- entry ------------------------------------------------------------------

if (($# == 1)) && [[ "$1" == "--self-test" ]]; then
  self_test
  exit 0
fi
(($# == 4)) || usage_fail
for arg in "$@"; do
  [[ "$arg" == -* ]] && usage_fail
done
build "$@"
