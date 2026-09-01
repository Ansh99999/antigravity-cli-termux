#!/usr/bin/env bash
# Exercises the transactional-update shell that lib/agy_helper.c builds, by
# putting a fake curl in front of it.
#
# The point is that this runs anywhere, including an x86 CI runner: the fixture
# archive's "binaries" are shell scripts, so what is under test is the
# staging/commit/rollback logic and the agy-provider handling, not the aarch64
# binaries themselves. Nothing here touches the network.
#
# Usage: scripts/update-harness.sh [workdir] [prebuilt-agy]
#   workdir       scratch directory (default: a mktemp -d, removed on exit)
#   prebuilt-agy  a compiled bootstrapper to test (default: build one here)
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ROOT=${1:-}
AGY_BIN=${2:-}

if [[ -z "$ROOT" ]]; then
  ROOT=$(mktemp -d "${TMPDIR:-/tmp}/agy-update-harness.XXXXXX")
  trap 'rm -rf "$ROOT"' EXIT
fi
mkdir -p "$ROOT"

pass=0
fail=0

note() { printf '  %s\n' "$*"; }
check() {
  local label=$1 expected=$2 actual=$3
  if [[ "$expected" == "$actual" ]]; then
    pass=$((pass + 1))
    note "ok   $label"
  else
    fail=$((fail + 1))
    note "FAIL $label: want [$expected] got [$actual]"
  fi
}

# ---------------------------------------------------------------------------
# The binary under test. Built here by default, so this is one command to run.
# ---------------------------------------------------------------------------
build_bootstrapper() {
  local out="$ROOT/agy-under-test"
  local cc=${CC:-cc}
  local shim="$ROOT/shim"

  # asm/hwcap.h only ships with the aarch64 kernel headers, and the source
  # guards its own HWCAP_ATOMICS, so an empty stand-in compiles on an x86 host.
  mkdir -p "$shim/asm"
  : >"$shim/asm/hwcap.h"

  "$cc" -std=gnu11 -D_GNU_SOURCE -O2 -I"$shim" \
    -DAGY_TERMUX_VERSION='"1.2.3"' \
    -o "$out" "$repo_root/lib/agy_helper.c" >&2
  printf '%s' "$out"
}

if [[ -z "$AGY_BIN" ]]; then
  printf 'Building the bootstrapper for the harness...\n'
  AGY_BIN=$(build_bootstrapper)
fi
printf 'Bootstrapper under test: %s\n' "$AGY_BIN"

# The tag the fake release serves. It has to compare higher than the version
# compiled in above, or the update is never offered in the first place.
export AGY_TEST_TAG="v9.9.9"

# ---------------------------------------------------------------------------
# A fixture archive whose members are scripts, plus a fake curl that serves it.
# ---------------------------------------------------------------------------
make_archive() {
  local dest=$1 helper=$2 agy_help_status=$3
  local stage="$ROOT/stage.$$"
  rm -rf "$stage"
  mkdir -p "$stage"

  printf '#!/bin/sh\necho NEW-AGY "$@"\nexit %s\n' "$agy_help_status" >"$stage/agy"
  printf '#!/bin/sh\necho NEW-PAYLOAD\n' >"$stage/agy.va39"
  chmod 0755 "$stage/agy" "$stage/agy.va39"

  local members=(agy agy.va39)
  case "$helper" in
    present)
      printf '#!/bin/sh\necho NEW-HELPER 9.9.9\n' >"$stage/agy-provider"
      ;;
    broken)
      # Fails the --version check the update uses to vet a staged helper.
      printf '#!/bin/sh\necho NEW-HELPER 9.9.9\nexit 1\n' >"$stage/agy-provider"
      ;;
  esac
  if [[ "$helper" != "absent" ]]; then
    chmod 0755 "$stage/agy-provider"
    members+=(agy-provider)
  fi

  tar -czf "$dest" -C "$stage" "${members[@]}"
  rm -rf "$stage"
}

make_fake_curl() {
  local bindir=$1 archive=$2
  cat >"$bindir/curl" <<'SH'
#!/usr/bin/env bash
# Two shapes are served: the redirect probe that resolves the latest release,
# and the archive download. Anything else is a test bug and must be loud.
#
# The probe's answer is derived from the URL it was handed, so this stays
# correct whatever repository the binary under test was compiled to follow.
set -Eeuo pipefail

out=""
latest=""
prev=""
want_url=0
for arg in "$@"; do
  [[ "$prev" == "-o" ]] && out=$arg
  [[ "$arg" == "-w" ]] && want_url=1
  [[ "$arg" == */releases/latest ]] && latest=$arg
  prev=$arg
done

if (( want_url )); then
  if [[ -z "$latest" ]]; then
    echo "fake curl: no latest-release URL in: $*" >&2
    exit 22
  fi
  printf '%s\n' "${latest%/latest}/tag/$AGY_TEST_TAG"
  exit 0
fi

if [[ -n "$out" && "$out" != "/dev/null" ]]; then
  cp "$AGY_TEST_ARCHIVE" "$out"
  exit 0
fi

echo "fake curl: unexpected invocation: $*" >&2
exit 22
SH
  chmod 0755 "$bindir/curl"
  export AGY_TEST_ARCHIVE="$archive"
}

# ---------------------------------------------------------------------------
# One case: a fresh fake Termux prefix and install dir, then the real
# `agy update -y` run through it. Echoes the exit status.
# ---------------------------------------------------------------------------
run_update() {
  local case_dir=$1 helper_in_archive=$2 helper_installed=$3 agy_help_status=$4 sabotage=$5

  rm -rf "$case_dir"
  mkdir -p "$case_dir/prefix/bin" "$case_dir/prefix/etc" "$case_dir/prefix/tmp" \
    "$case_dir/prefix/glibc/lib" "$case_dir/install"

  # What the bootstrapper insists on before it will do anything: a resolver
  # config, the glibc loader (checked in main() ahead of any subcommand), and a
  # qemu to fall back to when the host has no LSE atomics.
  : >"$case_dir/prefix/etc/resolv.conf"
  : >"$case_dir/prefix/glibc/lib/ld-linux-aarch64.so.1"
  : >"$case_dir/prefix/bin/qemu-aarch64"

  cp "$AGY_BIN" "$case_dir/install/agy"
  printf '#!/bin/sh\necho OLD-PAYLOAD\n' >"$case_dir/install/agy.va39"
  chmod 0755 "$case_dir/install/agy.va39"
  if [[ "$helper_installed" == "yes" ]]; then
    printf '#!/bin/sh\necho OLD-HELPER 1.0.0\n' >"$case_dir/install/agy-provider"
    chmod 0755 "$case_dir/install/agy-provider"
  fi

  make_archive "$case_dir/release.tar.gz" "$helper_in_archive" "$agy_help_status"
  make_fake_curl "$case_dir/prefix/bin" "$case_dir/release.tar.gz"

  # Removing the installed payload makes the `cp -p` of the old payload fail,
  # which is the step just past the point where the helper has been staged.
  if [[ "$sabotage" == "drop-payload" ]]; then
    rm -f "$case_dir/install/agy.va39"
  fi

  local status=0
  env -i \
    PATH="$case_dir/prefix/bin:/usr/bin:/bin" \
    HOME="$case_dir/home" \
    PREFIX="$case_dir/prefix" \
    TERMUX_VERSION="harness" \
    TMPDIR="$case_dir/prefix/tmp" \
    AGY_AUTO_UPDATE=1 \
    AGY_TEST_TAG="$AGY_TEST_TAG" \
    AGY_TEST_ARCHIVE="$AGY_TEST_ARCHIVE" \
    "$case_dir/install/agy" update -y >"$case_dir/out.txt" 2>&1 || status=$?
  printf '%s' "$status"
}

# describe reports what ended up installed, as one comparable line.
describe() {
  local install=$1
  local agy payload helper
  if grep -qF NEW-AGY "$install/agy" 2>/dev/null; then agy="NEW-AGY"; else agy="OLD-AGY"; fi
  payload=$(grep -o 'NEW-PAYLOAD\|OLD-PAYLOAD' "$install/agy.va39" 2>/dev/null | head -1)
  if [[ -e "$install/agy-provider" ]]; then
    helper=$(grep -o 'NEW-HELPER\|OLD-HELPER' "$install/agy-provider" | head -1)
  else
    helper="absent"
  fi
  printf '%s/%s/%s' "$agy" "${payload:-missing}" "$helper"
}

# leftovers reports any staging files the cleanup should have removed.
leftovers() {
  find "$1" -maxdepth 1 -name '.agy*' -printf '%f\n' | sort | tr '\n' ' '
}

# reported_failure says whether the run told the user the update did not apply.
# `agy update` exits 0 either way — that is long-standing behaviour and is not
# what this harness is here to change — so the message is what a caller has to
# go on, and it is what is asserted.
reported_failure() {
  if grep -qF 'Error: Update failed' "$1/out.txt"; then echo yes; else echo no; fi
}

# ---------------------------------------------------------------------------
# The cases.
# ---------------------------------------------------------------------------
printf '\n== A. the archive has the helper, one is installed ==\n'
status=$(run_update "$ROOT/case-a" present yes 0 none)
check "exit status" "0" "$status"
check "installed set" "NEW-AGY/NEW-PAYLOAD/NEW-HELPER" "$(describe "$ROOT/case-a/install")"
check "no staging leftovers" "" "$(leftovers "$ROOT/case-a/install")"

printf '\n== B. the archive predates the helper, one is installed ==\n'
status=$(run_update "$ROOT/case-b" absent yes 0 none)
check "exit status" "0" "$status"
check "the installed helper is left alone" "NEW-AGY/NEW-PAYLOAD/OLD-HELPER" \
  "$(describe "$ROOT/case-b/install")"
check "no staging leftovers" "" "$(leftovers "$ROOT/case-b/install")"

printf '\n== C. the archive has the helper, none is installed ==\n'
status=$(run_update "$ROOT/case-c" present no 0 none)
check "exit status" "0" "$status"
check "the helper arrives" "NEW-AGY/NEW-PAYLOAD/NEW-HELPER" "$(describe "$ROOT/case-c/install")"
check "no staging leftovers" "" "$(leftovers "$ROOT/case-c/install")"

printf '\n== D. the new agy fails its own --help: nothing may change ==\n'
run_update "$ROOT/case-d" present yes 1 none >/dev/null
check "failure reported" "yes" "$(reported_failure "$ROOT/case-d")"
check "installed set untouched" "OLD-AGY/OLD-PAYLOAD/OLD-HELPER" \
  "$(describe "$ROOT/case-d/install")"
check "no staging leftovers" "" "$(leftovers "$ROOT/case-d/install")"

printf '\n== E. failure after the helper is staged: rollback, helper untouched ==\n'
run_update "$ROOT/case-e" present yes 0 drop-payload >/dev/null
check "failure reported" "yes" "$(reported_failure "$ROOT/case-e")"
check "agy and the helper are the old ones" "OLD-AGY/missing/OLD-HELPER" \
  "$(describe "$ROOT/case-e/install")"
check "no staging leftovers" "" "$(leftovers "$ROOT/case-e/install")"

printf '\n== F. the same, with no helper installed: none may be left behind ==\n'
run_update "$ROOT/case-f" present no 0 drop-payload >/dev/null
check "failure reported" "yes" "$(reported_failure "$ROOT/case-f")"
check "no helper appears" "OLD-AGY/missing/absent" "$(describe "$ROOT/case-f/install")"
check "no staging leftovers" "" "$(leftovers "$ROOT/case-f/install")"

printf '\n== G. the helper in the archive will not run: the engine still updates ==\n'
status=$(run_update "$ROOT/case-g" broken yes 0 none)
check "exit status" "0" "$status"
check "the installed helper is kept" "NEW-AGY/NEW-PAYLOAD/OLD-HELPER" \
  "$(describe "$ROOT/case-g/install")"
check "the user is told" "yes" \
  "$(grep -qF 'agy-provider helper' "$ROOT/case-g/out.txt" && echo yes || echo no)"
check "no staging leftovers" "" "$(leftovers "$ROOT/case-g/install")"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
