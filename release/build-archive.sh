#!/usr/bin/env bash
# Builds one agentmesh release binary for a GOOS/GOARCH pair and, for linux,
# its tar.gz. The one build step shared by release-build.yml and the local
# dry run (release/test-pipeline.sh). Ported from the inline build and
# package steps of v1 .github/workflows/release-build.yml.
#
#   build-archive.sh <binary> <tag> <goos> <goarch> <outdir>
#   build-archive.sh --self-test
#
# <binary> is agentmesh or agentmesh-mcponly. Run it from the module root.
# Windows and darwin leave the bare binary in <outdir>: the zip is packaged
# after signing (package-windows) and lipo runs in release.sh.
set -euo pipefail

USAGE='usage: build-archive.sh <binary> <tag> <goos> <goarch> <outdir> | --self-test'
MODULE='github.com/ericfitz/agentmesh'
MODULE_VERSION_VAR='github.com/ericfitz/agentmesh/internal/version.Version'
BINARIES=(agentmesh agentmesh-mcponly)

usage_fail() {
  echo "$USAGE" >&2
  exit 2
}

build() {
  local binary="$1" tag="$2" goos="$3" goarch="$4" outdir="$5" version ext="" bin got hostos hostarch pkg want path
  case "$binary" in
    agentmesh)
      pkg=.
      want="$MODULE"
      ;;
    agentmesh-mcponly)
      pkg=./cmd/agentmesh-mcponly
      want="$MODULE/cmd/agentmesh-mcponly"
      ;;
    *)
      echo "build-archive.sh: binary must be agentmesh or agentmesh-mcponly, got '$binary'" >&2
      exit 2
      ;;
  esac
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
  bin="$outdir/$binary$ext"

  # open note: signing-keys 1: release archives are built without a key-provider tag (lean)
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags "-s -w -X ${MODULE_VERSION_VAR}=${version}" -o "$bin" "$pkg"

  # The binary must be the package asked for, also in a cross build (nothing
  # runs it there): the main package path is recorded in the build info.
  path="$(go version -m "$bin" | awk '$1 == "path" { print $2; exit }')"
  [[ "$path" == "$want" ]] || {
    echo "build-archive.sh: $bin holds the wrong binary: path $path, want $want" >&2
    rm -f "$bin" # never leave a binary that failed its check where a later step packs it
    exit 1
  }

  hostos="$(go env GOHOSTOS)"
  hostarch="$(go env GOHOSTARCH)"
  if [[ "$hostos" == "$goos" && "$hostarch" == "$goarch" ]]; then
    got="$("$bin" version)"
    [[ "$got" == "$version" ]] || {
      echo "version mismatch: $got != $version" >&2
      rm -f "$bin"
      exit 1
    }
    echo "ok: $bin prints $got"
  else
    echo "note: not run (host is $hostos/$hostarch)"
  fi

  if [[ "$goos" == linux ]]; then
    tar -C "$outdir" -czf "$outdir/${binary}-${tag}-linux-${goarch}.tar.gz" "$binary"
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
  local hostos hostarch ext="" rc=0 out listing b
  TMP_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_DIR"' EXIT
  local tmp="$TMP_DIR"
  SELF="${BASH_SOURCE[0]}"
  [[ "$SELF" == /* ]] || SELF="$PWD/$SELF"

  expect_refusal usage "$USAGE"
  expect_refusal too-many-args "$USAGE" agentmesh v0.0.1 linux amd64 "$tmp" extra
  expect_refusal four-args "$USAGE" v0.0.1 linux amd64 "$tmp"
  expect_refusal bad-flag "$USAGE" --bogus
  expect_refusal flag-in-position "$USAGE" agentmesh v0.0.1 -linux amd64 "$tmp"
  expect_refusal flag-as-binary "$USAGE" -agentmesh v0.0.1 linux amd64 "$tmp"
  expect_refusal bad-binary "binary must be agentmesh or agentmesh-mcponly" bogus v0.0.1 linux amd64 "$tmp"
  expect_refusal bad-binary-path "binary must be agentmesh or agentmesh-mcponly" ./agentmesh v0.0.1 linux amd64 "$tmp"
  expect_refusal bad-binary-exe "binary must be agentmesh or agentmesh-mcponly" agentmesh.exe v0.0.1 windows amd64 "$tmp"
  for b in "${BINARIES[@]}"; do
    expect_refusal "bad-tag-$b" "tag must look like vX.Y.Z" "$b" v1.2 linux amd64 "$tmp"
    expect_refusal "bad-tag-prerelease-$b" "tag must look like vX.Y.Z" "$b" v1.2.3-rc1 linux amd64 "$tmp"
    expect_refusal "bad-pair-$b" "plan9/amd64" "$b" v0.0.1 plan9 amd64 "$tmp"
    expect_refusal "bad-arch-$b" "linux/386" "$b" v0.0.1 linux 386 "$tmp"
  done

  # A binary that fails the package-path check is removed, not left in outdir
  # for a later step to pack (a go shim that builds a file and names another package).
  mkdir -p "$tmp/shim"
  cat >"$tmp/shim/go" <<'SHIM'
#!/bin/sh
case "$1" in
  build) while [ $# -gt 0 ]; do if [ "$1" = -o ]; then echo x >"$2"; exit 0; fi; shift; done ;;
  version) printf '%s: go1.0\n\tpath\texample.com/wrong\n' "$3" ;;
esac
SHIM
  chmod +x "$tmp/shim/go"
  rc=0
  out="$(PATH="$tmp/shim:$PATH" bash "$SELF" agentmesh v0.0.8 linux amd64 "$tmp/wrongbin" 2>&1)" || rc=$?
  if [[ "$rc" == 1 && "$out" == *"holds the wrong binary"* && ! -e "$tmp/wrongbin/agentmesh" ]]; then
    echo "PASS wrong-binary-removed"
    SELF_PASS=$((SELF_PASS + 1))
  else
    echo "FAIL wrong-binary-removed: exit $rc, left: $(ls "$tmp/wrongbin" 2>&1): $out"
    SELF_FAIL=$((SELF_FAIL + 1))
  fi

  hostos="$(go env GOHOSTOS)"
  hostarch="$(go env GOHOSTARCH)"
  [[ "$hostos" == windows ]] && ext=".exe"
  for b in "${BINARIES[@]}"; do
    case "$hostos/$hostarch" in
      linux/amd64 | linux/arm64 | windows/amd64 | windows/arm64 | darwin/amd64 | darwin/arm64)
        rc=0
        out="$(bash "$SELF" "$b" v0.0.7 "$hostos" "$hostarch" "$tmp/native" 2>&1)" || rc=$?
        if [[ "$rc" == 0 && "$(
          "$tmp/native/$b$ext" version
        )" == "0.0.7" ]]; then
          echo "PASS native-build-$b"
          SELF_PASS=$((SELF_PASS + 1))
        else
          echo "FAIL native-build-$b: exit $rc: $out"
          SELF_FAIL=$((SELF_FAIL + 1))
        fi
        ;;
      *)
        echo "FAIL native-build-$b: unsupported host $hostos/$hostarch"
        SELF_FAIL=$((SELF_FAIL + 1))
        ;;
    esac
  done

  # The archive binary is lean: no key-provider tag, so no sigstore module (and no cloud SDK behind one) is linked.
  # Each binary is the package asked for: the build info names it.
  local mods want_path
  for b in "${BINARIES[@]}"; do
    want_path="$MODULE"
    [[ "$b" == agentmesh-mcponly ]] && want_path="$MODULE/cmd/agentmesh-mcponly"
    if [[ -f "$tmp/native/$b$ext" ]]; then
      if mods="$(go version -m "$tmp/native/$b$ext" 2>&1)" && [[ "$mods" == *"agentmesh"* ]] \
        && [[ "$mods" != *sigstore* ]]; then
        echo "PASS archive-binary-links-no-sigstore-$b"
        SELF_PASS=$((SELF_PASS + 1))
      else
        echo "FAIL archive-binary-links-no-sigstore-$b: $(printf '%s' "$mods" | grep -i sigstore | head -3)"
        SELF_FAIL=$((SELF_FAIL + 1))
      fi
      if [[ "$(go version -m "$tmp/native/$b$ext" | awk '$1 == "path" { print $2; exit }')" == "$want_path" ]]; then
        echo "PASS binary-path-$b"
        SELF_PASS=$((SELF_PASS + 1))
      else
        echo "FAIL binary-path-$b: holds the wrong binary, want path $want_path"
        SELF_FAIL=$((SELF_FAIL + 1))
      fi
    else
      echo "FAIL archive-binary-links-no-sigstore-$b: no native binary at $tmp/native/$b$ext"
      SELF_FAIL=$((SELF_FAIL + 1))
    fi
  done

  # The linux tarball on any host: the native one on Linux, else a cross
  # build (not run here, so only the packaging is judged).
  local tarball tardir="$tmp/native" tararch="$hostarch"
  if [[ "$hostos" != linux ]]; then
    tardir="$tmp/linux"
    tararch=amd64
    for b in "${BINARIES[@]}"; do
      rc=0
      out="$(bash "$SELF" "$b" v0.0.7 linux amd64 "$tardir" 2>&1)" || rc=$?
      if [[ "$rc" == 0 && "$out" == *"note: not run (host is $hostos/$hostarch)"* ]]; then
        echo "PASS linux-cross-build-$b"
        SELF_PASS=$((SELF_PASS + 1))
      else
        echo "FAIL linux-cross-build-$b: exit $rc: $out"
        SELF_FAIL=$((SELF_FAIL + 1))
      fi
    done
  fi
  for b in "${BINARIES[@]}"; do
    tarball="$tardir/${b}-v0.0.7-linux-${tararch}.tar.gz"
    listing="$(tar -tzf "$tarball" 2>&1)" || true
    if [[ "$listing" == "$b" ]]; then
      echo "PASS linux-tar-lists-binary-$b"
      SELF_PASS=$((SELF_PASS + 1))
    else
      echo "FAIL linux-tar-lists-binary-$b: $listing"
      SELF_FAIL=$((SELF_FAIL + 1))
    fi
    # The packed binary is the one built, and executable.
    if [[ -f "$tarball" ]] && tar -xzOf "$tarball" "$b" | cmp -s - "$tardir/$b" \
      && [[ "$(tar -tvzf "$tarball" | cut -c1-10)" == -rwx* ]]; then
      echo "PASS linux-tar-holds-the-binary-$b"
      SELF_PASS=$((SELF_PASS + 1))
    else
      echo "FAIL linux-tar-holds-the-binary-$b: $tarball"
      SELF_FAIL=$((SELF_FAIL + 1))
    fi
  done

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
(($# == 5)) || usage_fail
for arg in "$@"; do
  [[ "$arg" == -* ]] && usage_fail
done
build "$@"
