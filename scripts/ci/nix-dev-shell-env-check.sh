#!/usr/bin/env bash
#
# Entering the Sky dev shell must not write the environment to disk (v0.27.7).
#
# stdenv's setup.sh runs `dumpVars` when a shell is entered: it writes every
# exported variable as `declare -x NAME=value` to `$NIX_BUILD_TOP/env-vars`.
# For `nix-shell -A shell` that directory is the caller's TMPDIR, so on a
# developer machine whose TMPDIR resolved to /tmp the dev shell copied the
# developer's whole environment, secrets included, into /tmp/env-vars.
# default.nix now sets `noDumpEnvVars` on the dev shell.
#
# This check enters both dev shells (`nix-shell -A shell`, `nix develop`) with a
# CLEAN environment (`env -i`), one dummy variable (SKY_DUMMY_SECRET, not a
# secret) and a scratch TMPDIR, then fails if any file under that TMPDIR is
# named `env-vars` or contains the dummy value. It never prints a variable.
#
#   scripts/ci/nix-dev-shell-env-check.sh                     the two dev shells
#   scripts/ci/nix-dev-shell-env-check.sh --with-build-check  also run
#       scripts/ci/nix-build-check.sh with the scratch TMPDIR and fail if it
#       leaves anything behind there
#
# nix-build-check.sh runs the first form, so the nightly `nix` job and the
# release `gate-nix` job run it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

source "$ROOT/scripts/lib/require-tool.sh"
require_tool nix-shell "install Nix (https://nixos.org/download)"
require_tool nix "install Nix (https://nixos.org/download)"

WITH_BUILD_CHECK=0
case "${1:-}" in
  "") ;;
  --with-build-check) WITH_BUILD_CHECK=1 ;;
  *)
    echo "usage: $0 [--with-build-check]" >&2
    exit 2
    ;;
esac

DUMMY_VALUE="not-a-secret-$$"
NIX_DIR="$(dirname "$(command -v nix)")"
NIX_SHELL_DIR="$(dirname "$(command -v nix-shell)")"
CLEAN_PATH="$NIX_DIR:$NIX_SHELL_DIR:/usr/bin:/bin:/usr/sbin:/sbin"

fail() {
  echo "::error::nix-dev-shell-env-check: $*" >&2
  exit 1
}

# A scratch TMPDIR per entry, removed on exit whatever happens.
SCRATCHES=()
cleanup() {
  local d
  for d in ${SCRATCHES[@]+"${SCRATCHES[@]}"}; do
    rm -rf "$d"
  done
}
trap cleanup EXIT

new_scratch() {
  local d
  d="$(mktemp -d "${TMPDIR:-/tmp}/sky-nix-env.XXXXXX")"
  chmod 700 "$d"
  SCRATCHES+=("$d")
  SCRATCH="$d"
}

# Fails when an env-vars file, or any file carrying this run's dummy value, sits
# under the scratch TMPDIR, or when /tmp/env-vars carries the dummy value.
# nix-shell does not always dump into TMPDIR: on macOS, with TMPDIR under
# /var/folders, its build top is /tmp, which is how the dump reached
# /private/tmp/env-vars on a developer machine. Names only; never the content.
# A dump this run made is removed before the check fails (it holds only the
# clean HOME, PATH, TMPDIR and dummy this check passes, plus the shell
# derivation's own variables).
assert_no_dump() {
  local label=$1 dir=$2 found f
  found="$({
    find "$dir" -name env-vars -print 2>/dev/null
    grep -rlF "$DUMMY_VALUE" "$dir" 2>/dev/null || true
  } | sort -u | head -5 | tr '\n' ' ')"
  for f in /tmp/env-vars /private/tmp/env-vars; do
    if [ -f "$f" ] && grep -qF "$DUMMY_VALUE" "$f" 2>/dev/null; then
      rm -f "$f"
      found="$found $f"
    fi
  done
  [ -z "$found" ] || fail "$label: wrote the environment to a file: $found"
  echo "$label: no environment written to disk"
}

# Runs "$@" with only HOME, PATH, TMPDIR, the Nix daemon settings Nix needs to
# find its store, and the dummy variable.
run_clean() {
  local tmp=$1
  shift
  env -i HOME="$HOME" PATH="$CLEAN_PATH" TMPDIR="$tmp" \
    ${NIX_REMOTE:+NIX_REMOTE="$NIX_REMOTE"} \
    ${NIX_SSL_CERT_FILE:+NIX_SSL_CERT_FILE="$NIX_SSL_CERT_FILE"} \
    SKY_DUMMY_SECRET="$DUMMY_VALUE" "$@"
}

new_scratch
run_clean "$SCRATCH" nix-shell -A shell --run true >/dev/null ||
  fail "nix-shell -A shell did not start"
assert_no_dump "nix-shell -A shell" "$SCRATCH"

new_scratch
run_clean "$SCRATCH" nix --extra-experimental-features 'nix-command flakes' develop -c true >/dev/null ||
  fail "nix develop did not start"
assert_no_dump "nix develop" "$SCRATCH"

if [ "$WITH_BUILD_CHECK" = 1 ]; then
  new_scratch
  # nix-build-check.sh works in a private TMPDIR of its own under this one and
  # removes it on exit: nothing may be left here afterwards.
  TMPDIR="$SCRATCH" "$ROOT/scripts/ci/nix-build-check.sh" >/dev/null ||
    fail "nix-build-check.sh failed"
  left="$(find "$SCRATCH" -mindepth 1 -print 2>/dev/null | head -5)"
  [ -z "$left" ] || fail "nix-build-check.sh left files in TMPDIR: $left"
  echo "nix-build-check.sh: TMPDIR left empty"
fi

echo "nix-dev-shell-env-check: PASS"
