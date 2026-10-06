#!/usr/bin/env bash
#
# Build Sky through both Nix entry points and check what each one produces.
#
#   stable  `nix-build` (default.nix, no flakes) and `nix-shell -A shell`
#   flake   `nix build .#sky` (the thin flake.nix wrapper) and `nix develop`
#
# For each build: `sky --version` prints exactly what Nix computed for that
# entry, `sky v<rust/Cargo.toml workspace version>`, plus ` (<rev>)` for the
# flake (its short git revision); and the `sky-ffi-inspect` installed beside
# it inspects a real Go package. Each dev shell must give cargo and go, and
# entering it must not write the environment to disk
# (scripts/ci/nix-dev-shell-env-check.sh). On a
# tag (GITHUB_REF_TYPE=tag) the stable build must print exactly `sky v<tag>`
# and the flake build must start with it, so a version source that disagrees
# with the tag fails the release.
#
# Run by nightly-sweep.yml (`nix`) and release.yml (`gate-nix`). Locally it
# needs a Nix with the `nix` command; it enables flakes for its own calls.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

source "$ROOT/scripts/lib/require-tool.sh"
require_tool nix-build "install Nix (https://nixos.org/download)"
require_tool nix "install Nix (https://nixos.org/download)"

NIX=(nix --extra-experimental-features 'nix-command flakes')

# Every nix-build / nix-shell / nix build / nix develop below runs with a
# private TMPDIR (mode 700) that is removed on exit. stdenv can write the
# whole environment to `$TMPDIR/env-vars` when a shell is entered (the dev
# shell now sets `noDumpEnvVars`; see default.nix), so a future dump, or any
# other temp file Nix leaves, never lands in a shared /tmp.
PRIVATE_TMPDIR="$(mktemp -d "${TMPDIR:-/tmp}/sky-nix-build-check.XXXXXX")"
chmod 700 "$PRIVATE_TMPDIR"
trap 'rm -rf "$PRIVATE_TMPDIR"' EXIT
export TMPDIR="$PRIVATE_TMPDIR"

fail() {
  echo "::error::nix-build-check: $*" >&2
  exit 1
}

# Sets GOT to what the build's `sky --version` printed.
check_build() {
  local label=$1 link=$2 want=$3
  GOT="$("$link/bin/sky" --version)"
  echo "$label: sky --version -> $GOT (want $want)"
  [ "$GOT" = "$want" ] || fail "$label: \`sky --version\` printed '$GOT', expected '$want'"
  [ -x "$link/bin/sky-ffi-inspect" ] || fail "$label: $link/bin/sky-ffi-inspect is missing"
}

# The inspector needs a Go on PATH; each dev shell supplies one.
check_inspector() {
  local label=$1 link=$2
  shift 2
  local out
  out="$("$@" "$link/bin/sky-ffi-inspect strings")"
  printf '%s' "$out" | grep -q '"ToUpper"' ||
    fail "$label: sky-ffi-inspect strings did not describe strings.ToUpper: $(printf '%s' "$out" | head -c 300)"
  echo "$label: sky-ffi-inspect strings -> $(printf '%s' "$out" | wc -c | tr -d ' ') bytes of JSON"
}

# ---- stable entry -----------------------------------------------------------
version="$("${NIX[@]}" eval --raw --file . version)"
nix-build --out-link result-stable
check_build stable result-stable "sky v$version"
stable_got=$GOT
nix-shell -A shell --run 'cargo --version && go version'
check_inspector stable result-stable nix-shell -A shell --run

# ---- flake entry ------------------------------------------------------------
flake_rev="$("${NIX[@]}" eval --raw .#sky.gitRev)"
flake_want="sky v$version"
[ -n "$flake_rev" ] && flake_want="sky v$version ($flake_rev)"
"${NIX[@]}" build .#sky --out-link result-flake
check_build flake result-flake "$flake_want"
flake_got=$GOT
"${NIX[@]}" develop -c sh -c 'cargo --version && go version'
check_inspector flake result-flake "${NIX[@]}" develop -c sh -c

# ---- entering a dev shell writes no environment to disk ---------------------
"$ROOT/scripts/ci/nix-dev-shell-env-check.sh"

# ---- a tag: the version is the tag ------------------------------------------
if [ "${GITHUB_REF_TYPE:-}" = tag ]; then
  tag="${GITHUB_REF_NAME#v}"
  [ "$stable_got" = "sky v$tag" ] ||
    fail "tag v$tag, but the nix-build binary prints '$stable_got': the version source disagrees with the tag"
  case "$flake_got" in
    "sky v$tag" | "sky v$tag ("*) ;;
    *) fail "tag v$tag, but the flake binary prints '$flake_got'" ;;
  esac
fi

echo "nix-build-check: both entry points build, report their version and run sky-ffi-inspect"
