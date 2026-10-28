#!/usr/bin/env bash
#
# scripts/ai/trainer/build.sh - build sky-trainer, the native LoRA trainer of
# Std.Ai.Train, against a pinned MLX and mlx-c. The contributor bootstrap: the
# release bundles are built from the same sources, in CI only, by
# build-trainer-bundle.sh (.github/workflows/ai-trainer-bundles.yml).
#
#   build.sh --mlx <prefix> --mlx-c <src> [--out <dir>]
#
#   --mlx    an MLX install: include/mlx/version.h, lib/libmlx.{dylib,so} and,
#            on macOS, lib/mlx.metallib. Its version must be $MLX_PIN.
#   --mlx-c  a git checkout of ml-explore/mlx-c at $MLXC_PIN.
#   --out    where to put sky-trainer and lib/ (default: a fresh directory
#            under $TMPDIR). Nothing is written inside the repository.
#
# The trainer and the mlx-c library are compiled with the system C compiler
# (/usr/bin/clang on macOS, cc elsewhere; CC and CXX override). The output is
# self-contained: sky-trainer finds lib/ by rpath. The last line printed is the
# trainer's path.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
source "$ROOT/scripts/lib/with-timeout.sh"

MLX_PIN="0.32.4"
MLXC_PIN="57731b00e"

MLX="" MLXC="" OUT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --mlx) MLX="$2"; shift 2 ;;
    --mlx-c) MLXC="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    *) echo "usage: build.sh --mlx <prefix> --mlx-c <src> [--out <dir>]" >&2; exit 2 ;;
  esac
done
[ -n "$MLX" ] && [ -n "$MLXC" ] || { echo "usage: build.sh --mlx <prefix> --mlx-c <src> [--out <dir>]" >&2; exit 2; }

ver_h="$MLX/include/mlx/version.h"
[ -f "$ver_h" ] || { echo "build.sh: $ver_h is missing; --mlx must be an MLX $MLX_PIN install" >&2; exit 1; }
ver="$(for k in MAJOR MINOR PATCH; do sed -n "s/^#define MLX_VERSION_$k \([0-9]*\).*/\1/p" "$ver_h"; done | paste -sd. -)"
[ "$ver" = "$MLX_PIN" ] || { echo "build.sh: --mlx is MLX $ver; the trainer is pinned to $MLX_PIN" >&2; exit 1; }
head="$(git -C "$MLXC" rev-parse HEAD 2>/dev/null || true)"
case "$head" in
  "$MLXC_PIN"*) ;;
  *) echo "build.sh: --mlx-c must be a git checkout of mlx-c at $MLXC_PIN (found ${head:-no git checkout})" >&2; exit 1 ;;
esac

case "$(uname -s)" in
  Darwin)
    CC="${CC:-/usr/bin/clang}"; CXX="${CXX:-/usr/bin/clang++}"
    DYLIB=dylib; RPATH='-Wl,-rpath,@executable_path/lib'; LOADER_RPATH='-Wl,-rpath,@loader_path'
    INSTALL_NAME='-install_name @rpath/libmlxc.dylib' ;;
  *)
    CC="${CC:-cc}"; CXX="${CXX:-c++}"
    DYLIB=so; RPATH="-Wl,-rpath,\$ORIGIN/lib"; LOADER_RPATH="-Wl,-rpath,\$ORIGIN"
    INSTALL_NAME='-Wl,-soname,libmlxc.so' ;;
esac

OUT="${OUT:-$(mktemp -d "${TMPDIR:-/tmp}/sky-trainer.XXXXXX")}"
mkdir -p "$OUT/lib" "$OUT/obj"

# mlx-c: one object per source file, two at a time (a 16 GB machine builds
# other things too), then one shared library. Each worker is a fresh bash, so
# the timeout shim is exported to it.
export -f with_timeout _sky_with_timeout_which _sky_with_timeout_resolve
export _SKY_WITH_TIMEOUT_PERL_PROG SKY_WITH_TIMEOUT_KILL_AFTER
start=$(date +%s)
for f in "$MLXC"/mlx/c/*.cpp; do
  o="$OUT/obj/$(basename "$f" .cpp).o"
  if [ ! -f "$o" ] || [ "$f" -nt "$o" ]; then echo "$f"; fi
done | xargs -P2 -I{} bash -c '
  f="$1"; out="$2"; mlx="$3"; mlxc="$4"; cxx="$5"
  with_timeout 900 "$cxx" -std=c++20 -O2 -fPIC -DNDEBUG -I "$mlxc" -I "$mlx/include" -c "$f" -o "$out/obj/$(basename "$f" .cpp).o"
' _ {} "$OUT" "$MLX" "$MLXC" "$CXX"
# shellcheck disable=SC2086
with_timeout 600 "$CXX" -shared -o "$OUT/lib/libmlxc.$DYLIB" "$OUT"/obj/*.o -L"$MLX/lib" -lmlx $LOADER_RPATH $INSTALL_NAME

for lib in "$MLX"/lib/libmlx*."$DYLIB"* "$MLX"/lib/libjaccl*."$DYLIB"* "$MLX"/lib/mlx.metallib; do
  [ -e "$lib" ] || continue
  if [ ! -e "$OUT/lib/$(basename "$lib")" ] || ! cmp -s "$lib" "$OUT/lib/$(basename "$lib")"; then
    /bin/cp -f "$lib" "$OUT/lib/"
  fi
done

# shellcheck disable=SC2086
with_timeout 600 "$CC" -O2 -std=c11 -Wall -Wextra -Wno-unused-parameter -DSKY_MLX_VERSION="\"$MLX_PIN\"" \
  -I "$MLXC" "$HERE/sky_trainer.c" "$HERE/guard.c" "$HERE/sha256.c" -o "$OUT/sky-trainer" \
  -L "$OUT/lib" -lmlxc -lmlx -lm $RPATH
echo "build.sh: sky-trainer built in $(( $(date +%s) - start ))s" >&2
echo "$OUT/sky-trainer"
