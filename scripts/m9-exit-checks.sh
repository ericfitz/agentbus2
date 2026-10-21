#!/usr/bin/env bash
# Milestone 9 exit checks: the automated half of the release exit. Part 1 reads
# the real repository (installers, workflows, templates, the shared helpers);
# part 2 applies four mutations in a throwaway worktree of HEAD and proves the
# check each one targets fires. It is the self-test of the release tooling
# (Definition of Done C) and runs as the m9-exit-selftest CI job.
#   usage: m9-exit-checks.sh [--real-only]     (--real-only skips the mutations)
# Output is the check-gates.sh contract: "ok  <name>" or "FAIL <name>: <why>";
# exit 1 on any FAIL, 2 on a bad argument. Signing, notarization, the tagged
# release and publishing are Milestone 9 Tasks 9 and 10 (Eric's): they print as
# "pending", never as ok.
#
# It tests HEAD for part 2: commit changes to release/, install.sh and the
# workflows before running it. State: a second run starts clean (the worktree
# and the temp dir are removed on exit); overlapping runs use separate temp
# dirs and worktree paths; a killed run leaves a registered worktree and a temp
# dir under $TMPDIR, and `git worktree prune` (run before adding ours) clears
# the registration.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SELF="${BASH_SOURCE[0]}"
USAGE='usage: m9-exit-checks.sh [--real-only]'
MOD=github.com/ericfitz/agentbus2

REAL_ONLY=0
case "$#:${1:-}" in
0:) ;;
1:--real-only) REAL_ONLY=1 ;;
*)
	echo "$USAGE" >&2
	exit 2
	;;
esac

tmp="$(mktemp -d)"
wt="$tmp/wt"
cleanup() {
	git -C "$ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || true
	git -C "$ROOT" worktree prune
	rm -rf "$tmp"
}
trap cleanup EXIT

bad=0
pass() { echo "ok  $1"; }
fail() {
	echo "FAIL $1: $2"
	bad=1
}
# squash FILE: the tail of a command's output on one line, for a FAIL message.
squash() { tail -n 4 "$1" | tr '\n' ' '; }

# run_capture OUTFILE CMD...: run CMD with combined output in OUTFILE; sets rc.
rc=0
run_capture() {
	local out="$1"
	shift
	rc=0
	"$@" >"$out" 2>&1 || rc=$?
}

BEGIN_KEY='# BEGIN agentbus2 release public key'
END_KEY='# END agentbus2 release public key'
PLACEHOLDER_PEM='REPLACED-BY-release/embed-key.sh'

# --- part 1: the real repository -----------------------------------------------

check_pins_real() {
	local n=pins-real-repo out="$tmp/pins.out"
	run_capture "$out" bash "$ROOT/release/check-pins.sh" "$ROOT/.github/workflows"
	if [ "$rc" -eq 0 ] && grep -qF 'check-pins: OK' "$out"; then pass "$n"; else fail "$n" "exit $rc: $(squash "$out")"; fi
}

# unembed IN OUT: IN with its key block reset to the placeholder (a .sh or a
# .ps1 block), so the case does not depend on whether the real key is embedded.
unembed() {
	local in="$1" out="$2"
	awk -v b="$BEGIN_KEY" -v e="$END_KEY" -v ext="${in##*.}" '
		$0 == b { print
			if (ext == "ps1") { print "$PubKeyPem = @'"'"'"; print "-----BEGIN PUBLIC KEY-----"; print "REPLACED-BY-release/embed-key.sh"; print "-----END PUBLIC KEY-----"; print "'"'"'@" }
			else { print "PUBKEY_PEM='"'"'-----BEGIN PUBLIC KEY-----"; print "REPLACED-BY-release/embed-key.sh"; print "-----END PUBLIC KEY-----'"'"'" }
			skip = 1; next }
		$0 == e { skip = 0 }
		!skip { print }
	' "$in" >"$out"
}

# The four cases that read the repository's own installers (release/test-release.sh
# keeps the synthetic ones, named *-synthetic-*).
check_embedded_cases() {
	local ossl pub s ext script name_un name_em out
	if ! ossl="$(source "$ROOT/release/release.sh" && release_openssl)"; then
		for s in real-unembedded ps1-unembedded real-embedded ps1-embedded; do fail "$s" "no OpenSSL 3 (release_openssl failed)"; done
		return
	fi
	"$ossl" genpkey -algorithm ed25519 -out "$tmp/tw.pem" 2>/dev/null
	pub="$tmp/tw.pub"
	"$ossl" pkey -in "$tmp/tw.pem" -pubout -out "$pub" 2>/dev/null
	for ext in sh ps1; do
		script="install.$ext"
		name_un=real-unembedded
		name_em=real-embedded
		if [ "$ext" = ps1 ]; then
			name_un=ps1-unembedded
			name_em=ps1-embedded
		fi
		out="$tmp/emb-$ext.out"
		unembed "$ROOT/$script" "$tmp/$script"
		run_capture "$out" bash -c "source '$ROOT/release/release.sh'; check_script_embedded '$tmp/$script' $script"
		if [ "$rc" -eq 1 ] && grep -qF "$script has no embedded release key" "$out"; then pass "$name_un"; else fail "$name_un" "exit $rc, want 1 with '$script has no embedded release key': $(squash "$out")"; fi
		if bash "$ROOT/release/embed-key.sh" "$pub" "$tmp/$script" >/dev/null 2>"$out"; then
			run_capture "$out" bash -c "source '$ROOT/release/release.sh'; check_script_embedded '$tmp/$script' $script"
			if [ "$rc" -eq 0 ]; then pass "$name_em"; else fail "$name_em" "exit $rc, want 0: $(squash "$out")"; fi
		else
			fail "$name_em" "embed-key.sh failed: $(squash "$out")"
		fi
	done
}

# key_block_of SCRIPT: prints the lines between the markers; returns 1 with a
# reason on stderr unless each marker occurs once, in order.
key_block_of() {
	local s="$1" nb ne lb le
	nb="$(grep -cxF "$BEGIN_KEY" "$s" || true)"
	ne="$(grep -cxF "$END_KEY" "$s" || true)"
	if [ "$nb" != 1 ] || [ "$ne" != 1 ]; then
		echo "$s holds the BEGIN marker $nb time(s) and the END marker $ne time(s), want once each" >&2
		return 1
	fi
	lb="$(grep -nxF "$BEGIN_KEY" "$s" | cut -d: -f1)"
	le="$(grep -nxF "$END_KEY" "$s" | cut -d: -f1)"
	if [ "$lb" -ge "$le" ]; then
		echo "$s has the END marker before the BEGIN marker" >&2
		return 1
	fi
	sed -n "$((lb + 1)),$((le - 1))p" "$s"
}

check_installers_key_block() {
	local n=installers-key-block pub="$ROOT/release/agentbus2-release-ed25519.pub" s block want_ph want_pub ok=1
	for s in install.sh install.ps1; do
		if ! block="$(key_block_of "$ROOT/$s" 2>"$tmp/kb.err")"; then
			fail "$n" "$(cat "$tmp/kb.err")"
			ok=0
			continue
		fi
		if [ "$s" = install.sh ]; then
			want_ph="$(printf "PUBKEY_PEM='-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----'" "$PLACEHOLDER_PEM")"
		else
			want_ph="$(printf "\$PubKeyPem = @'\n-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----\n'@" "$PLACEHOLDER_PEM")"
		fi
		if [ "$block" = "$want_ph" ]; then continue; fi
		if [ ! -r "$pub" ]; then
			fail "$n" "$s holds neither the placeholder nor the committed key, and $pub does not exist"
			ok=0
			continue
		fi
		if [ "$s" = install.sh ]; then want_pub="PUBKEY_PEM='$(cat "$pub")'"; else want_pub="$(printf "\$PubKeyPem = @'\n%s\n'@" "$(cat "$pub")")"; fi
		if [ "$block" != "$want_pub" ]; then
			fail "$n" "the key block of $s differs from $pub and is not the placeholder"
			ok=0
		fi
	done
	if [ "$ok" -eq 1 ]; then pass "$n"; fi
	return 0
}

check_shared_helpers() {
	local n=shared-helpers-identical f nb ne ok=1
	for f in release/release.sh release/test-install.sh release/test-release.sh; do
		nb="$(grep -cxF '# BEGIN shared release helpers' "$ROOT/$f" || true)"
		ne="$(grep -cxF '# END shared release helpers' "$ROOT/$f" || true)"
		if [ "$nb" != 1 ] || [ "$ne" != 1 ]; then
			fail "$n" "$f holds $nb BEGIN and $ne END markers, want exactly one block"
			ok=0
			continue
		fi
		awk '/^# BEGIN shared release helpers$/ {p=1; next} /^# END shared release helpers$/ {p=0} p' "$ROOT/$f" >"$tmp/shared.$(basename "$f")"
	done
	if [ "$ok" -eq 1 ]; then
		if cmp -s "$tmp/shared.release.sh" "$tmp/shared.test-install.sh" && cmp -s "$tmp/shared.release.sh" "$tmp/shared.test-release.sh" && [ -s "$tmp/shared.release.sh" ]; then
			pass "$n"
		else
			fail "$n" "the three shared helper blocks differ (or are empty)"
		fi
	fi
}

check_templates_render() {
	local n=templates-render out="$tmp/render.out"
	run_capture "$out" bash "$ROOT/release/test-render.sh"
	if [ "$rc" -eq 0 ]; then pass "$n"; else fail "$n" "exit $rc: $(squash "$out")"; fi
}

check_pipeline_selftest() {
	local n=pipeline-selftest out="$tmp/pipeline-self.out"
	run_capture "$out" bash "$ROOT/release/test-pipeline.sh" --self-test
	if [ "$rc" -eq 0 ] && grep -qF 'test-pipeline self-test: OK' "$out"; then pass "$n"; else fail "$n" "exit $rc: $(squash "$out")"; fi
}

FORLINE='for a in linux-amd64.tar.gz linux-arm64.tar.gz windows-amd64.zip windows-arm64.zip; do'

# anchors_missing WORKFLOW: prints each literal anchor missing from WORKFLOW
# (a copy of release-build.yml) and from the two scripts of the repository.
anchors_missing() {
	local wf="$1" a
	for a in \
		'run-name: release-build ${{ inputs.tag }}' \
		'pattern: archive-*' \
		'name: archive-windows-amd64' \
		'name: archive-windows-arm64' \
		'name: archive-${{ matrix.goos }}-${{ matrix.goarch }}' \
		'-windows-amd64.zip"' \
		'-windows-arm64.zip"' \
		"$FORLINE"; do
		grep -qF -- "$a" "$wf" || echo "release-build.yml lacks: $a"
	done
	grep -qF -- '-linux-${goarch}.tar.gz' "$ROOT/release/build-archive.sh" || echo 'build-archive.sh lacks: -linux-${goarch}.tar.gz'
	grep -qF -- "$FORLINE" "$ROOT/release/release.sh" || echo "release.sh download_archives lacks: $FORLINE"
}

check_workflow_names() {
	local n=workflow-names-agree miss
	sed 's/windows-arm64\.zip/windows-arm46.zip/g' "$ROOT/.github/workflows/release-build.yml" >"$tmp/planted.yml"
	if [ -z "$(anchors_missing "$tmp/planted.yml")" ]; then
		fail "$n" "anchor check misses a planted wrong name"
		return
	fi
	miss="$(anchors_missing "$ROOT/.github/workflows/release-build.yml")"
	if [ -z "$miss" ]; then pass "$n"; else fail "$n" "$(echo "$miss" | tr '\n' ';')"; fi
}

V1_PATTERN='AGENTBUS_[A-Z]|agentbus-v|ericfitz/agentbus([^2]|$)|Programs\\agentbus([^2]|$)|/\.agentbus\.tmp'

check_no_v1_names() {
	local n=no-v1-names files=() f out="$tmp/v1.out" planted
	for planted in 'url: https://github.com/ericfitz/agentbus' '"$env:LOCALAPPDATA\Programs\agentbus"'; do
		printf '%s\n' "$planted" >"$tmp/planted.txt"
		if ! rg -q -e "$V1_PATTERN" "$tmp/planted.txt"; then
			fail "$n" "regex misses the planted v1 line"
			return
		fi
	done
	for f in install.sh install.ps1 release/*.sh release/*.tmpl release/windows-zip-README.txt \
		.github/workflows/release-build.yml .github/workflows/windows.yml .github/workflows/publish-image.yml; do
		if [ -e "$ROOT/$f" ]; then files+=("$ROOT/$f"); fi
	done
	if rg -n -e "$V1_PATTERN" "${files[@]}" >"$out" 2>&1; then
		fail "$n" "v1 names found: $(squash "$out")"
	else
		pass "$n"
	fi
}

check_version_var() {
	local n=version-var-exists want="$MOD/internal/version.Version" f ok=1 listed
	for f in release/build-archive.sh Dockerfile; do
		[ -e "$ROOT/$f" ] || continue
		if ! grep -qF -- "$want" "$ROOT/$f"; then
			fail "$n" "$f does not set $want"
			ok=0
		fi
	done
	if ! listed="$(cd "$ROOT" && go list -f '{{.ImportPath}}' ./internal/version 2>&1)" || [ "$listed" != "$MOD/internal/version" ]; then
		fail "$n" "go list ./internal/version printed: $listed"
		ok=0
	fi
	if ! rg -q -e '^var Version' "$ROOT/internal/version/version.go"; then
		fail "$n" "internal/version/version.go has no 'var Version'"
		ok=0
	fi
	if [ "$ok" -eq 1 ]; then pass "$n"; fi
	return 0
}

check_usage() {
	local n=usage-rejects-unknown out="$tmp/usage.out"
	run_capture "$out" bash "$SELF" --bogus
	if [ "$rc" -eq 2 ] && grep -qF 'usage:' "$out"; then pass "$n"; else fail "$n" "exit $rc, want 2 with usage: $(squash "$out")"; fi
}

check_pins_real
check_embedded_cases
check_installers_key_block
check_shared_helpers
check_templates_render
check_pipeline_selftest
check_workflow_names
check_no_v1_names
check_version_var
check_usage

# --- part 2: mutations in a throwaway worktree of HEAD ---------------------------

# mutate NAME FILE SEDEXPR: apply the sed expression to FILE in the worktree;
# returns 1 (after a FAIL) when the file did not change.
mutate() {
	local n="$1" f="$wt/$2"
	cp "$f" "$tmp/before"
	sed "$3" "$tmp/before" >"$f"
	if cmp -s "$tmp/before" "$f"; then
		fail "$n" "mutation changed nothing"
		return 1
	fi
}
revert() { git -C "$wt" checkout -q -- .; }

# expect NAME WANT_RC OUT TEXT: the last run_capture must have WANT_RC and TEXT in OUT.
expect() {
	local n="$1" want="$2" out="$3" text="$4"
	if [ "$rc" -ne "$want" ]; then
		fail "$n" "exit $rc, want $want: $(squash "$out")"
	elif ! grep -qF -- "$text" "$out"; then
		fail "$n" "output lacks '$text': $(squash "$out")"
	else
		pass "$n"
	fi
}

mutations() {
	local out="$tmp/mut.out" first
	git -C "$ROOT" worktree prune
	git -C "$ROOT" worktree add --detach "$wt" HEAD >/dev/null

	# (1) an unpinned action: the first pinned uses: SHA becomes @v4
	first="$(grep -nE '@[0-9a-f]{40}' "$wt/.github/workflows/release-build.yml" | head -n 1 | cut -d: -f1)"
	if mutate mutation-unpinned-action .github/workflows/release-build.yml "${first:-0}s/@[0-9a-f]\\{40\\}/@v4/"; then
		run_capture "$out" bash "$wt/release/check-pins.sh" "$wt/.github/workflows"
		expect mutation-unpinned-action 1 "$out" 'release-build.yml:'
	fi
	revert

	# (2) an unfilled placeholder in a template
	if mutate mutation-unfilled-placeholder release/agentbus2.rb.tmpl '$a\
extra "__UNFILLED__"'; then
		run_capture "$out" bash "$wt/release/test-render.sh"
		expect mutation-unfilled-placeholder 1 "$out" 'unfilled placeholders'
	fi
	revert

	# (3) the END marker of install.sh deleted: key-block runs before build_fixture
	if mutate mutation-key-block-end install.sh "/^$END_KEY\$/d"; then
		run_capture "$out" bash "$wt/release/test-install.sh" --self-test
		expect mutation-key-block-end 1 "$out" 'FAIL key-block'
	fi
	revert

	# (4) a wrong -X path: the binary keeps 2.0.0-dev and the dry run notices
	if mutate mutation-version-var release/build-archive.sh 's#internal/version\.Version#internal/version.Nope#g'; then
		run_capture "$out" bash "$wt/release/test-pipeline.sh"
		expect mutation-version-var 1 "$out" 'version mismatch'
	fi
	revert
}

if [ "$REAL_ONLY" -eq 0 ]; then
	mutations
fi

# Not automated: Eric's Tasks 9 and 10. Printed so the exit never reads as
# complete without them.
for p in 'Authenticode signing of the Windows archives' 'macOS signing and notarization (notarytool profile sqdist-notary)' \
	'the tagged release (git tag, release-build run, attestations)' 'the Homebrew tap and Scoop bucket pushes' \
	'the winget submission (komac)' 'the image push (publish-image.yml)'; do
	echo "pending $p: Milestone 9 Tasks 9 and 10, Eric"
done

if [ "$bad" -ne 0 ]; then
	echo "m9-exit-checks: FAILED"
	exit 1
fi
echo "m9-exit-checks: OK"
