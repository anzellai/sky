#!/usr/bin/env bash
#
# scripts/ai/trainer/build-trainer-bundle.sh - build one sky-trainer release
# bundle (the native LoRA trainer of Std.Ai.Train) from pinned, hash-checked
# sources. CI runs it (.github/workflows/ai-trainer-bundles.yml); a contributor
# on a Mac builds the trainer with build.sh instead.
#
#   build-trainer-bundle.sh <darwin-arm64|linux-x64> --out <dir>
#                           [--sources <dir>] [--work <dir>] [--jobs N]
#                           [--metal-jit]
#
#   darwin-arm64  MLX with the Metal backend (and its CPU backend on
#                 Accelerate); lib/mlx.metallib holds the Metal kernels.
#   linux-x64     MLX with the CPU backend only, on OpenBLAS (BLAS and LAPACK)
#                 linked statically into libmlx.so; built on ubuntu-22.04, so
#                 the glibc floor is 2.35.
#
#   --out       where the archive, its SBOM and its .sha256 are written.
#   --sources   the download cache (default <work>/sources). A cached file is
#               used only if its SHA-256 is the pin.
#   --work      the build directory (default: a fresh one under $TMPDIR).
#   --jobs      parallel compile jobs (default 4).
#   --metal-jit build MLX with MLX_METAL_JIT=ON (darwin only): the kernels are
#               compiled at run time and the metallib is small. A measurement
#               build: its archive name says jit, and the workflow never
#               publishes it.
#
# The bundle: sky-trainer-<version>-<platform>/ with sky-trainer, lib/ (libmlxc,
# libmlx, on darwin mlx.metallib), licenses/, NOTICE.txt and sbom.spdx.json.
# Before it is archived, every Mach-O or ELF object in it is walked: each
# dependency must be in the bundle or on the platform's system list, so a host
# BLAS, libgomp or libgfortran fails the build. `sky-trainer --version` must
# print the version ai_pins.go pins.
#
# Every source is fetched by URL and checked against the SHA-256 below, and
# MLX's own CMake downloads are pointed at those checked trees with
# FETCHCONTENT_FULLY_DISCONNECTED, so the build fetches nothing unpinned.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"

# ─── the pins ───────────────────────────────────────────────────────────
# MLX_PIN and MLXC_PIN are build.sh's; the workflows_parse test holds the two
# files to one value.
MLX_PIN="0.32.4"
MLX_URL="https://github.com/ml-explore/mlx/archive/refs/tags/v0.32.4.tar.gz"
MLX_SHA256="0276db8d7bf1af8db6d4ae25c7c550abea4d5b3b6a16207610a5d4a0c19fe1a0"
MLXC_PIN="57731b00e"
MLXC_COMMIT="57731b00e8bed710e0750e7356da9f18f8eb3e20"
MLXC_URL="https://github.com/ml-explore/mlx-c/archive/57731b00e8bed710e0750e7356da9f18f8eb3e20.tar.gz"
MLXC_SHA256="55014245c4ba580c44a1e98a59c514c5e51732ad8f2485fb7ccc7781a17bac6f"
# MLX's FetchContent dependencies, at the versions MLX 0.32.4 declares.
JSON_VERSION="3.11.3"
JSON_URL="https://github.com/nlohmann/json/releases/download/v3.11.3/json.tar.xz"
JSON_SHA256="d6c65aca6b1ed68e7a182f4757257b107ae403032760ed6ef121c9d55e81757d"
FMT_VERSION="12.1.0"
FMT_URL="https://github.com/fmtlib/fmt/archive/refs/tags/12.1.0.tar.gz"
FMT_SHA256="ea7de4299689e12b6dddd392f9896f08fb0777ac7168897a244a6d6085043fea"
METALCPP_VERSION="26"
METALCPP_URL="https://developer.apple.com/metal/cpp/files/metal-cpp_26.zip"
METALCPP_SHA256="4df3c078b9aadcb516212e9cb03004cbc5ce9a3e9c068fa3144d021db585a3a4"
OPENBLAS_VERSION="0.3.33"
OPENBLAS_URL="https://github.com/OpenMathLib/OpenBLAS/releases/download/v0.3.33/OpenBLAS-0.3.33.tar.gz"
OPENBLAS_SHA256="6761af1d9f5d353ab4f0b7497be2643313b36c8f31caec0144bfef198e71e6ab"

usage() {
  echo "usage: build-trainer-bundle.sh <darwin-arm64|linux-x64> --out <dir> [--sources <dir>] [--work <dir>] [--jobs N] [--metal-jit]" >&2
  exit 2
}

PLATFORM="${1:-}"
[ -n "$PLATFORM" ] || usage
shift
OUT="" SOURCES="" WORK="" JOBS=4 JIT=0
while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --sources) SOURCES="$2"; shift 2 ;;
    --work) WORK="$2"; shift 2 ;;
    --jobs) JOBS="$2"; shift 2 ;;
    --metal-jit) JIT=1; shift ;;
    *) usage ;;
  esac
done
[ -n "$OUT" ] || usage

case "$PLATFORM" in
  darwin-arm64) want_os=Darwin want_arch=arm64 VARIANT=metal ;;
  linux-x64) want_os=Linux want_arch=x86_64 VARIANT=cpu ;;
  *) echo "build-trainer-bundle: the platform is darwin-arm64 or linux-x64, not '$PLATFORM'" >&2; exit 2 ;;
esac
if [ "$(uname -s)" != "$want_os" ] || [ "$(uname -m)" != "$want_arch" ]; then
  echo "build-trainer-bundle: $PLATFORM builds on $want_os $want_arch, not $(uname -s) $(uname -m)" >&2
  exit 1
fi
if [ "$JIT" = 1 ]; then
  [ "$PLATFORM" = darwin-arm64 ] || { echo "build-trainer-bundle: --metal-jit is for darwin-arm64" >&2; exit 2; }
  VARIANT=metal-jit
fi

# The version every bundle carries is the one the runtime pins.
VERSION="$(sed -n 's/^const aiTrainerVersion = "\(.*\)"$/\1/p' "$ROOT/runtime-go/rt/ai_pins.go")"
[ -n "$VERSION" ] || { echo "build-trainer-bundle: no aiTrainerVersion in runtime-go/rt/ai_pins.go" >&2; exit 1; }
grep -q "^#define SKY_TRAINER_VERSION \"$VERSION\"" "$HERE/sky_trainer.c" ||
  { echo "build-trainer-bundle: sky_trainer.c's SKY_TRAINER_VERSION is not ai_pins.go's $VERSION" >&2; exit 1; }
for pin in "MLX_PIN=\"$MLX_PIN\"" "MLXC_PIN=\"$MLXC_PIN\""; do
  grep -qx "$pin" "$HERE/build.sh" || { echo "build-trainer-bundle: build.sh does not pin $pin" >&2; exit 1; }
done
case "$MLXC_COMMIT" in "$MLXC_PIN"*) ;; *) echo "build-trainer-bundle: MLXC_COMMIT is not MLXC_PIN" >&2; exit 1 ;; esac

require_tool curl "install curl"
require_tool tar "install tar"
require_tool shasum "install shasum (perl)"
require_tool unzip "install unzip"
require_tool cmake "install CMake 3.25+"
require_tool ninja "install Ninja"
require_tool jq "install jq (the SBOM)"
if [ "$PLATFORM" = linux-x64 ]; then
  require_tool make "install make"
  require_tool gcc "install gcc"
  require_tool readelf "install binutils"
else
  require_tool xcrun "install the Xcode command line tools"
  require_tool otool "install the Xcode command line tools"
fi

WORK="${WORK:-$(mktemp -d "${TMPDIR:-/tmp}/sky-trainer-bundle.XXXXXX")}"
SOURCES="${SOURCES:-$WORK/sources}"
mkdir -p "$WORK" "$SOURCES" "$OUT"
WORK="$(cd "$WORK" && pwd)" SOURCES="$(cd "$SOURCES" && pwd)" OUT="$(cd "$OUT" && pwd)"
SRC="$WORK/src"
rm -rf "$SRC" && mkdir -p "$SRC"
start=$(date +%s)

# fetch <url> <sha256> <file>: the file in the cache, checked; a cached file
# with another digest is fetched again, and a fetched one that does not match
# fails the build.
fetch() {
  local url="$1" sha="$2" f="$SOURCES/$3"
  if [ -f "$f" ] && echo "$sha  $f" | shasum -a 256 -c - > /dev/null 2>&1; then return 0; fi
  rm -f "$f"
  with_timeout 600 curl -fsSL --retry 3 -o "$f.part" "$url"
  echo "$sha  $f.part" | shasum -a 256 -c - > /dev/null || {
    echo "build-trainer-bundle: $url does not have the pinned SHA-256 $sha" >&2
    rm -f "$f.part"
    exit 1
  }
  mv -f "$f.part" "$f"
}

fetch "$MLX_URL" "$MLX_SHA256" "mlx-$MLX_PIN.tar.gz"
fetch "$MLXC_URL" "$MLXC_SHA256" "mlx-c-$MLXC_COMMIT.tar.gz"
fetch "$JSON_URL" "$JSON_SHA256" "json-$JSON_VERSION.tar.xz"
fetch "$FMT_URL" "$FMT_SHA256" "fmt-$FMT_VERSION.tar.gz"
tar -xzf "$SOURCES/mlx-$MLX_PIN.tar.gz" -C "$SRC"
tar -xzf "$SOURCES/mlx-c-$MLXC_COMMIT.tar.gz" -C "$SRC"
mkdir -p "$SRC/json" && tar -xJf "$SOURCES/json-$JSON_VERSION.tar.xz" -C "$SRC/json" --strip-components 1
tar -xzf "$SOURCES/fmt-$FMT_VERSION.tar.gz" -C "$SRC"
MLX_SRC="$SRC/mlx-$MLX_PIN"
MLXC_SRC="$SRC/mlx-c-$MLXC_COMMIT"
if [ "$PLATFORM" = darwin-arm64 ]; then
  fetch "$METALCPP_URL" "$METALCPP_SHA256" "metal-cpp_$METALCPP_VERSION.zip"
  unzip -q "$SOURCES/metal-cpp_$METALCPP_VERSION.zip" -d "$SRC"
  [ -d "$SRC/metal-cpp" ] || { echo "build-trainer-bundle: metal-cpp_$METALCPP_VERSION.zip has no metal-cpp/" >&2; exit 1; }
else
  fetch "$OPENBLAS_URL" "$OPENBLAS_SHA256" "OpenBLAS-$OPENBLAS_VERSION.tar.gz"
  tar -xzf "$SOURCES/OpenBLAS-$OPENBLAS_VERSION.tar.gz" -C "$SRC"
fi
v="$(for k in MAJOR MINOR PATCH; do sed -n "s/^#define MLX_VERSION_$k \([0-9]*\).*/\1/p" "$MLX_SRC/mlx/version.h"; done | paste -sd. -)"
[ "$v" = "$MLX_PIN" ] || { echo "build-trainer-bundle: the MLX source is $v, not $MLX_PIN" >&2; exit 1; }

NAME="sky-trainer-$VERSION-$PLATFORM"
[ "$JIT" = 1 ] && NAME="$NAME-jit"
BUNDLE="$WORK/$NAME"
rm -rf "$BUNDLE" && mkdir -p "$BUNDLE/lib" "$BUNDLE/licenses"

# ─── the build ──────────────────────────────────────────────────────────
CMAKE_COMMON=(-G Ninja -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_LIBDIR=lib)
MLX_FLAGS=(
  -DBUILD_SHARED_LIBS=ON -DMLX_BUILD_TESTS=OFF -DMLX_BUILD_EXAMPLES=OFF
  -DMLX_BUILD_BENCHMARKS=OFF -DMLX_BUILD_PYTHON_BINDINGS=OFF -DMLX_BUILD_PYTHON_STUBS=OFF
  -DMLX_BUILD_GGUF=OFF -DMLX_BUILD_SAFETENSORS=ON -DMLX_BUILD_CPU=ON -DMLX_USE_CCACHE=OFF
  -DFETCHCONTENT_FULLY_DISCONNECTED=ON
  -DFETCHCONTENT_SOURCE_DIR_JSON="$SRC/json"
  -DFETCHCONTENT_SOURCE_DIR_FMT="$SRC/fmt-$FMT_VERSION"
)
if [ "$PLATFORM" = darwin-arm64 ]; then
  MLX_FLAGS+=(-DMLX_BUILD_METAL=ON -DCMAKE_OSX_DEPLOYMENT_TARGET=14.0
    -DFETCHCONTENT_SOURCE_DIR_METAL_CPP="$SRC/metal-cpp")
  if [ "$JIT" = 1 ]; then MLX_FLAGS+=(-DMLX_METAL_JIT=ON); else MLX_FLAGS+=(-DMLX_METAL_JIT=OFF); fi
  TRAINER_FLAGS=(-DCMAKE_OSX_DEPLOYMENT_TARGET=14.0 -DCMAKE_C_COMPILER=/usr/bin/clang -DCMAKE_CXX_COMPILER=/usr/bin/clang++)
else
  # OpenBLAS: BLAS and LAPACK (C_LAPACK: no Fortran, so no libgfortran),
  # static, position-independent (it goes into libmlx.so), with kernels for
  # every x86-64 CPU picked at run time (DYNAMIC_ARCH) over a PRESCOTT
  # baseline, pthreads and no OpenMP (no libgomp).
  OB_FLAGS=(NO_SHARED=1 NOFORTRAN=1 C_LAPACK=1 DYNAMIC_ARCH=1 TARGET=PRESCOTT
    USE_OPENMP=0 USE_THREAD=1 NUM_THREADS=64 NO_AFFINITY=1)
  OB_PREFIX="$WORK/openblas"
  with_timeout 3600 make -C "$SRC/OpenBLAS-$OPENBLAS_VERSION" -j"$JOBS" "${OB_FLAGS[@]}" libs netlib > "$WORK/openblas-build.log" 2>&1 ||
    { tail -60 "$WORK/openblas-build.log" >&2; exit 1; }
  with_timeout 600 make -C "$SRC/OpenBLAS-$OPENBLAS_VERSION" "${OB_FLAGS[@]}" PREFIX="$OB_PREFIX" install > "$WORK/openblas-install.log" 2>&1 ||
    { tail -60 "$WORK/openblas-install.log" >&2; exit 1; }
  [ -f "$OB_PREFIX/lib/libopenblas.a" ] || { echo "build-trainer-bundle: OpenBLAS built no libopenblas.a" >&2; exit 1; }
  # A static libopenblas.a needs libm on Linux (OpenBLAS's own Makefile adds
  # EXTRALIB += -lm), and FindBLAS/FindLAPACK's link test of a static OpenBLAS
  # adds only the thread libraries, so sgemm_ and cheev_ "are not found". The
  # checks link executables, and MLX builds none here (tests and examples are
  # off), so the flag reaches the configure checks only. --no-as-needed keeps
  # libm although it comes before the archive on the check's command line.
  # libmlx.so itself gets libm from the C++ driver.
  MLX_FLAGS+=(-DMLX_BUILD_METAL=OFF -DBLA_VENDOR=OpenBLAS -DBLA_STATIC=ON
    -DCMAKE_PREFIX_PATH="$OB_PREFIX"
    "-DCMAKE_EXE_LINKER_FLAGS=-Wl,--no-as-needed -lm"
    -DLAPACK_INCLUDE_DIRS="$OB_PREFIX/include" -DBLAS_INCLUDE_DIRS="$OB_PREFIX/include")
  TRAINER_FLAGS=()
fi
MLX_PREFIX="$WORK/mlx-install"
# A failed configure names the symbols its link checks could not resolve.
with_timeout 600 cmake -S "$MLX_SRC" -B "$WORK/build-mlx" "${CMAKE_COMMON[@]}" "${MLX_FLAGS[@]}" -DCMAKE_INSTALL_PREFIX="$MLX_PREFIX" || {
  grep -ho "undefined reference to [^ ]*" "$WORK/build-mlx/CMakeFiles/CMakeConfigureLog.yaml" 2>/dev/null | sort | uniq -c | sort -rn | head -20 >&2
  exit 1
}
with_timeout 7200 cmake --build "$WORK/build-mlx" -j "$JOBS"
with_timeout 600 cmake --install "$WORK/build-mlx"
with_timeout 600 cmake -S "$HERE" -B "$WORK/build-trainer" "${CMAKE_COMMON[@]}" ${TRAINER_FLAGS[@]+"${TRAINER_FLAGS[@]}"} \
  -DMLX_PREFIX="$MLX_PREFIX" -DMLXC_SRC="$MLXC_SRC"
with_timeout 1800 cmake --build "$WORK/build-trainer" -j "$JOBS"
with_timeout 600 cmake --install "$WORK/build-trainer" --prefix "$BUNDLE"
MLX_LIBDIR="$MLX_PREFIX/lib"

# ─── the libraries: exactly the closure the trainer loads ───────────────
# deps <object>: the libraries an object names. On darwin the @rpath ones are
# bundle libraries; on linux every NEEDED name is checked below.
deps() {
  if [ "$PLATFORM" = darwin-arm64 ]; then
    otool -L "$1" | tail -n +2 | awk '{print $1}'
  else
    readelf -dW "$1" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'
  fi
}
is_system() {
  if [ "$PLATFORM" = darwin-arm64 ]; then
    case "$1" in
      /usr/lib/libSystem.B.dylib | /usr/lib/libc++.1.dylib | /usr/lib/libobjc.A.dylib) return 0 ;;
      /System/Library/Frameworks/Metal.framework/* | /System/Library/Frameworks/Foundation.framework/* | \
        /System/Library/Frameworks/QuartzCore.framework/* | /System/Library/Frameworks/Accelerate.framework/* | \
        /System/Library/Frameworks/CoreFoundation.framework/*) return 0 ;;
    esac
  else
    case "$1" in
      libc.so.6 | libm.so.6 | libstdc++.so.6 | libgcc_s.so.1 | libpthread.so.0 | libdl.so.2 | librt.so.1 | ld-linux-x86-64.so.2) return 0 ;;
    esac
  fi
  return 1
}
bundled_name() {
  case "$1" in
    @rpath/*) echo "${1#@rpath/}" ;;
    /* | @*) return 1 ;;
    *) echo "$1" ;;
  esac
}
# Copy the bundle libraries the trainer reaches, from MLX's lib/, until the
# set is closed.
changed=1
while [ "$changed" = 1 ]; do
  changed=0
  for obj in "$BUNDLE/sky-trainer" "$BUNDLE"/lib/*; do
    case "$obj" in *.metallib) continue ;; esac
    while IFS= read -r d; do
      [ -n "$d" ] || continue
      is_system "$d" && continue
      n="$(bundled_name "$d")" || { echo "build-trainer-bundle: $(basename "$obj") loads $d, outside the bundle" >&2; exit 1; }
      [ "$n" = "$(basename "$obj")" ] && continue
      if [ ! -e "$BUNDLE/lib/$n" ]; then
        [ -e "$MLX_LIBDIR/$n" ] || { echo "build-trainer-bundle: $(basename "$obj") loads $n, which is neither a system library nor in $MLX_LIBDIR" >&2; exit 1; }
        /bin/cp -fL "$MLX_LIBDIR/$n" "$BUNDLE/lib/$n"
        changed=1
      fi
    done < <(deps "$obj")
  done
done
if [ "$PLATFORM" = darwin-arm64 ]; then
  [ -f "$MLX_LIBDIR/mlx.metallib" ] || { echo "build-trainer-bundle: no mlx.metallib in $MLX_LIBDIR" >&2; exit 1; }
  /bin/cp -f "$MLX_LIBDIR/mlx.metallib" "$BUNDLE/lib/"
fi
chmod 0755 "$BUNDLE/sky-trainer" "$BUNDLE"/lib/lib*
chmod 0644 "$BUNDLE"/lib/*.metallib 2>/dev/null || true

# ─── the walk: every object's dependencies are bundled or system ────────
bad=0
for obj in "$BUNDLE/sky-trainer" "$BUNDLE"/lib/lib*; do
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    is_system "$d" && continue
    if n="$(bundled_name "$d")" && { [ "$n" = "$(basename "$obj")" ] || [ -e "$BUNDLE/lib/$n" ]; }; then continue; fi
    echo "::error::$(basename "$obj") depends on $d, which the bundle does not carry and the system list does not name" >&2
    bad=1
  done < <(deps "$obj")
done
if [ "$PLATFORM" = linux-x64 ]; then
  # No host BLAS and no OpenMP or Fortran runtime: OpenBLAS is in libmlx.so.
  for obj in "$BUNDLE"/lib/lib*; do
    if deps "$obj" | grep -Eq 'openblas|blas|lapack|gomp|gfortran|quadmath'; then
      echo "::error::$(basename "$obj") loads a BLAS, OpenMP or Fortran library: $(deps "$obj" | tr '\n' ' ')" >&2
      bad=1
    fi
  done
  readelf -sW "$BUNDLE/lib/libmlx.so" | grep -q ' cblas_sgemm$' ||
    { echo "::error::libmlx.so does not carry OpenBLAS (no cblas_sgemm)" >&2; bad=1; }
  # The glibc floor: no symbol newer than GLIBC_2.35 (ubuntu-22.04).
  newest="$(for o in "$BUNDLE/sky-trainer" "$BUNDLE"/lib/lib*; do readelf -VW "$o"; done |
    grep -o 'GLIBC_2\.[0-9]*' | sed 's/GLIBC_2\.//' | sort -n | tail -1)"
  if [ -n "$newest" ] && [ "$newest" -gt 35 ]; then
    echo "::error::the bundle needs GLIBC_2.$newest; the floor is GLIBC_2.35" >&2
    bad=1
  fi
  # The rpaths: the trainer finds lib/, libmlxc finds libmlx beside itself.
  readelf -dW "$BUNDLE/sky-trainer" | grep -Eq '(RUNPATH|RPATH).*\[\$ORIGIN/lib\]' ||
    { echo "::error::sky-trainer's rpath is not \$ORIGIN/lib" >&2; bad=1; }
  readelf -dW "$BUNDLE/lib/libmlxc.so" | grep -Eq '(RUNPATH|RPATH).*\[\$ORIGIN\]' ||
    { echo "::error::libmlxc.so's rpath is not \$ORIGIN" >&2; bad=1; }
else
  otool -l "$BUNDLE/sky-trainer" | grep -A2 LC_RPATH | grep -q '@executable_path/lib' ||
    { echo "::error::sky-trainer's rpath is not @executable_path/lib" >&2; bad=1; }
fi
[ "$bad" = 0 ] || exit 1

got="$("$BUNDLE/sky-trainer" --version)"
[ "$got" = "sky-trainer $VERSION (mlx-c)" ] || { echo "build-trainer-bundle: --version printed '$got', want sky-trainer $VERSION" >&2; exit 1; }

# ─── licences, notice, SBOM ─────────────────────────────────────────────
/bin/cp -f "$MLX_SRC/LICENSE" "$BUNDLE/licenses/LICENSE-mlx"
/bin/cp -f "$MLXC_SRC/LICENSE" "$BUNDLE/licenses/LICENSE-mlx-c"
/bin/cp -f "$SRC/json/LICENSE.MIT" "$BUNDLE/licenses/LICENSE-nlohmann-json"
/bin/cp -f "$SRC/fmt-$FMT_VERSION/LICENSE" "$BUNDLE/licenses/LICENSE-fmt"
if [ "$PLATFORM" = darwin-arm64 ]; then
  /bin/cp -f "$SRC/metal-cpp/LICENSE.txt" "$BUNDLE/licenses/LICENSE-metal-cpp"
else
  /bin/cp -f "$SRC/OpenBLAS-$OPENBLAS_VERSION/LICENSE" "$BUNDLE/licenses/LICENSE-OpenBLAS"
  /bin/cp -f "$SRC/OpenBLAS-$OPENBLAS_VERSION/lapack-netlib/LICENSE" "$BUNDLE/licenses/LICENSE-LAPACK"
fi

{
  echo "sky-trainer $VERSION ($PLATFORM, $VARIANT) - the native LoRA trainer of Sky's Std.Ai.Train."
  echo "Built from scripts/ai/trainer/ in https://github.com/anzellai/sky, from these sources,"
  echo "each fetched by URL and checked by its SHA-256:"
  echo
  echo "  MLX $MLX_PIN (MIT)                    $MLX_URL"
  echo "      sha256 $MLX_SHA256"
  echo "  mlx-c $MLXC_COMMIT (MIT)"
  echo "      $MLXC_URL"
  echo "      sha256 $MLXC_SHA256"
  echo "  nlohmann/json $JSON_VERSION (MIT, compiled into libmlx)"
  echo "      sha256 $JSON_SHA256"
  echo "  fmt $FMT_VERSION (MIT, header-only, compiled into libmlx)"
  echo "      sha256 $FMT_SHA256"
  if [ "$PLATFORM" = darwin-arm64 ]; then
    echo "  metal-cpp $METALCPP_VERSION (Apache-2.0, headers compiled into libmlx)"
    echo "      sha256 $METALCPP_SHA256"
  else
    echo "  OpenBLAS $OPENBLAS_VERSION (BSD-3-Clause, with reference LAPACK; linked statically into libmlx.so)"
    echo "      sha256 $OPENBLAS_SHA256"
  fi
  echo
  echo "sky-trainer's own sources (sky_trainer.c, guard.c) are Sky's (Apache-2.0, licenses/LICENSE-sky); sha256.c is in the public domain."
  echo "The licence texts are in licenses/. sbom.spdx.json lists every file and every source."
} > "$BUNDLE/NOTICE.txt"
/bin/cp -f "$ROOT/LICENSE" "$BUNDLE/licenses/LICENSE-sky"

files_json="$(cd "$BUNDLE" && find . -type f ! -name sbom.spdx.json | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do
    jq -n --arg f "$f" --arg s "$(shasum -a 256 "$f" | cut -c1-64)" \
      '{fileName: ("./" + $f), SPDXID: ("SPDXRef-File-" + ($f | gsub("[^A-Za-z0-9.-]"; "-"))), checksums: [{algorithm: "SHA256", checksumValue: $s}]}'
  done | jq -s .)"
  pkg() { jq -n --arg n "$1" --arg v "$2" --arg u "$3" --arg s "$4" --arg l "$5" \
    '{name: $n, SPDXID: ("SPDXRef-Package-" + ($n | gsub("[^A-Za-z0-9.-]"; "-"))), versionInfo: $v, downloadLocation: $u,
      checksums: [{algorithm: "SHA256", checksumValue: $s}], licenseConcluded: $l, licenseDeclared: $l, copyrightText: "NOASSERTION", filesAnalyzed: false}'; }
  pkgs="$( {
    pkg mlx "$MLX_PIN" "$MLX_URL" "$MLX_SHA256" MIT
    pkg mlx-c "$MLXC_COMMIT" "$MLXC_URL" "$MLXC_SHA256" MIT
    pkg nlohmann-json "$JSON_VERSION" "$JSON_URL" "$JSON_SHA256" MIT
    pkg fmt "$FMT_VERSION" "$FMT_URL" "$FMT_SHA256" MIT
    if [ "$PLATFORM" = darwin-arm64 ]; then
      pkg metal-cpp "$METALCPP_VERSION" "$METALCPP_URL" "$METALCPP_SHA256" Apache-2.0
    else
      pkg OpenBLAS "$OPENBLAS_VERSION" "$OPENBLAS_URL" "$OPENBLAS_SHA256" BSD-3-Clause
    fi
  } | jq -s .)"
  jq -n --arg name "$NAME" --arg created "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson files "$files_json" --argjson pkgs "$pkgs" \
    '{spdxVersion: "SPDX-2.3", dataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT", name: $name,
      documentNamespace: ("https://github.com/anzellai/sky/spdx/" + $name),
      creationInfo: {created: $created, creators: ["Tool: scripts/ai/trainer/build-trainer-bundle.sh"]},
      packages: $pkgs, files: $files}' > "$BUNDLE/sbom.spdx.json"

# ─── the archive ────────────────────────────────────────────────────────
ARCHIVE="$OUT/$NAME.tar.gz"
# Fixed order, owner and mtime, so a rebuild of the same tree archives the same.
(cd "$WORK" && find "$NAME" -print | LC_ALL=C sort > "$WORK/files.txt")
if tar --version 2>/dev/null | grep -q GNU; then
  tar -C "$WORK" --owner=0 --group=0 --numeric-owner --mtime='@0' --no-recursion -T "$WORK/files.txt" -cf - | gzip -n -9 > "$ARCHIVE"
else
  COPYFILE_DISABLE=1 tar -C "$WORK" --uid 0 --gid 0 --no-recursion -T "$WORK/files.txt" -cf - | gzip -n -9 > "$ARCHIVE"
fi
/bin/cp -f "$BUNDLE/sbom.spdx.json" "$OUT/$NAME.sbom.spdx.json"
(cd "$OUT" && shasum -a 256 "$NAME.tar.gz" > "$NAME.tar.gz.sha256")

raw="$(du -sk "$BUNDLE" | cut -f1)"
echo "build-trainer-bundle: $NAME in $(($(date +%s) - start))s; unpacked ${raw} KiB, archive $(wc -c < "$ARCHIVE" | tr -d ' ') bytes" >&2
for f in "$BUNDLE"/lib/*; do echo "  lib/$(basename "$f") $(wc -c < "$f" | tr -d ' ') bytes" >&2; done
echo "$ARCHIVE"
