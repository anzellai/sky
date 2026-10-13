#!/usr/bin/env bash
# build-device-bundles.sh — build the Std.Ai.Device engines from the pinned
# llama.cpp source: the browser worker engine (WebGPU and CPU WebAssembly),
# the Android JNI library (arm64-v8a, x86_64) and the iOS xcframework
# (ios-arm64 with Metal, ios-arm64-simulator on the CPU).
#
# The ai-device-bundles workflow runs this to publish the release
# `sky-llama-b11200`; a contributor runs it to build the same artefacts
# locally (`sky ai provision --device <target> --from <out>` installs them).
#
# usage: build-device-bundles.sh <web|android|ios|all> --llama-src <dir> --out <dir>
#            [--emsdk <dir>] [--emdawnwebgpu <dir>] [--android-sdk <dir>] [--jobs <n>]
#
# Pinned toolchain (the workflow installs exactly these):
#   llama.cpp   b11200, source tarball sha256 77d9d947…6b6a94
#   emsdk       6.0.12
#   Dawn        emdawnwebgpu_pkg v20260423.175430, zip sha256 3ad7ebe9…10e732f
#   Android NDK 27.0.12077973, platform android-24
#   Xcode       the runner's default (iOS deployment target 17.0)
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
# shellcheck source=../../lib/with-timeout.sh
source "$ROOT/scripts/lib/with-timeout.sh"

BUILD=b11200
TARGET="${1:-}"
shift || true
LLAMA_SRC="" OUT="" EMSDK="" DAWN="" ANDROID_SDK="${ANDROID_HOME:-$HOME/Library/Android/sdk}" JOBS=4
while [ $# -gt 0 ]; do
    case "$1" in
        --llama-src) LLAMA_SRC="$2"; shift 2 ;;
        --out) OUT="$2"; shift 2 ;;
        --emsdk) EMSDK="$2"; shift 2 ;;
        --emdawnwebgpu) DAWN="$2"; shift 2 ;;
        --android-sdk) ANDROID_SDK="$2"; shift 2 ;;
        --jobs) JOBS="$2"; shift 2 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done
case "$TARGET" in web|android|ios|all) ;; *)
    echo "usage: build-device-bundles.sh <web|android|ios|all> --llama-src <dir> --out <dir> [--emsdk <dir>] [--emdawnwebgpu <dir>] [--android-sdk <dir>] [--jobs <n>]" >&2
    exit 2 ;;
esac
[ -n "$LLAMA_SRC" ] && [ -f "$LLAMA_SRC/include/llama.h" ] || { echo "--llama-src must be a llama.cpp $BUILD source tree" >&2; exit 2; }
[ -n "$OUT" ] || { echo "--out is required" >&2; exit 2; }
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
WORK="$OUT/.work"
mkdir -p "$WORK"
# Every tool runs in the work directory: a compiler probe that writes
# a.out leaves it there, not in the caller's tree.
cd "$WORK"
NDK_VERSION=27.0.12077973

notice() {
    local dir="$1"
    cp -f "$LLAMA_SRC/LICENSE" "$dir/LICENSE-llama.cpp"
    cp -f "$HERE/NOTICE.txt" "$dir/NOTICE.txt"
}

# cmake_build <dir> <cmake args…>: configure and build the skyllama target,
# bounded so a hung toolchain fails the build.
cmake_build() {
    local dir="$1"; shift
    with_timeout 1800 cmake -S "$HERE" -B "$dir" -G Ninja -DCMAKE_BUILD_TYPE=Release \
        -DLLAMA_SRC="$LLAMA_SRC" "$@" > "$dir.configure.log" 2>&1 \
        || { tail -40 "$dir.configure.log"; exit 1; }
    with_timeout 3600 cmake --build "$dir" -j "$JOBS" > "$dir.build.log" 2>&1 \
        || { grep -E 'error' "$dir.build.log" | head -40; tail -20 "$dir.build.log"; exit 1; }
}

build_web() {
    [ -n "$EMSDK" ] && [ -f "$EMSDK/emsdk_env.sh" ] || { echo "--emsdk must be an emsdk 6.0.12 checkout" >&2; exit 2; }
    export EMSDK_QUIET=1
    # shellcheck disable=SC1091
    source "$EMSDK/emsdk_env.sh" >/dev/null 2>&1
    # emsdk's own Python first on PATH: a `python3` found elsewhere can be a
    # shim that re-executes itself (a configure step that probes the Python
    # version then never returns).
    local py
    py="$(ls -d "$EMSDK"/python/*/bin 2>/dev/null | head -1)"
    [ -n "$py" ] && export PATH="$py:$PATH"
    local v
    v="$(emcc --version | head -1)"
    case "$v" in *" 6.0.12 "*|*"6.0.12"*) ;; *) echo "emsdk 6.0.12 is required, got: $v" >&2; exit 2 ;; esac
    local common=("-DSKY_TARGET=web" "-DCMAKE_C_FLAGS=-fwasm-exceptions" "-DCMAKE_CXX_FLAGS=-fwasm-exceptions"
        "-DCMAKE_EXE_LINKER_FLAGS=-fwasm-exceptions" "-DLLAMA_WASM_MEM64=OFF")
    local dawn=()
    [ -n "$DAWN" ] && dawn=("-DEMDAWNWEBGPU_DIR=$DAWN")
    # The CPU build.
    cmake_build "$WORK/web-cpu" -DCMAKE_TOOLCHAIN_FILE="$EMSDK/upstream/emscripten/cmake/Modules/Platform/Emscripten.cmake" \
        "${common[@]}" -DGGML_WEBGPU=OFF
    # The WebGPU build: JavaScript Promise Integration lets the exported
    # calls wait on the GPU.
    cmake_build "$WORK/web-webgpu" -DCMAKE_TOOLCHAIN_FILE="$EMSDK/upstream/emscripten/cmake/Modules/Platform/Emscripten.cmake" \
        "${common[@]}" -DGGML_WEBGPU=ON "${dawn[@]}" ${py:+"-DPython3_EXECUTABLE=$py/python3"} \
        "-DSKY_EXTRA_LINK=-sJSPI_EXPORTS=sky_llama_init,sky_llama_load,sky_llama_adapter,sky_llama_chat,sky_llama_chat_begin,sky_llama_chat_next,sky_llama_tokenize,sky_llama_unload"
    rm -rf "$OUT/web" && mkdir -p "$OUT/web/cpu" "$OUT/web/webgpu"
    cp -f "$WORK/web-cpu/skyllama.mjs" "$WORK/web-cpu/skyllama.wasm" "$OUT/web/cpu/"
    cp -f "$WORK/web-webgpu/skyllama.mjs" "$WORK/web-webgpu/skyllama.wasm" "$OUT/web/webgpu/"
    notice "$OUT/web"
    (cd "$OUT" && tar -czf "sky-llama-$BUILD-web.tar.gz" web)
}

build_android() {
    local ndk="$ANDROID_SDK/ndk/$NDK_VERSION"
    [ -f "$ndk/build/cmake/android.toolchain.cmake" ] || { echo "Android NDK $NDK_VERSION is required under $ANDROID_SDK/ndk" >&2; exit 2; }
    local strip
    strip="$(ls -d "$ndk"/toolchains/llvm/prebuilt/*/bin/llvm-strip | head -1)"
    rm -rf "$OUT/android" && mkdir -p "$OUT/android"
    for abi in arm64-v8a x86_64; do
        local arch=()
        # armv8.2 dot product and fp16 on arm64 (every 64-bit Android phone
        # since 2018); the x86_64 build is for the emulator.
        [ "$abi" = arm64-v8a ] && arch=("-DGGML_CPU_ARM_ARCH=armv8.2-a+dotprod+fp16")
        cmake_build "$WORK/android-$abi" -DSKY_TARGET=android \
            -DCMAKE_TOOLCHAIN_FILE="$ndk/build/cmake/android.toolchain.cmake" \
            -DANDROID_ABI="$abi" -DANDROID_PLATFORM=android-24 -DANDROID_STL=c++_static \
            -DGGML_NATIVE=OFF -DGGML_LLAMAFILE=OFF "${arch[@]}"
        mkdir -p "$OUT/android/$abi"
        "$strip" --strip-unneeded -o "$OUT/android/$abi/libskyllama.so" "$WORK/android-$abi/libskyllama.so"
    done
    notice "$OUT/android"
    (cd "$OUT" && tar -czf "sky-llama-$BUILD-android.tar.gz" android)
}

build_ios() (
    # Apple's own tools, by absolute path: another clang or xcrun earlier on
    # PATH (a Nix or Homebrew toolchain) targets macOS and cannot build iOS.
    local xcrun=/usr/bin/xcrun
    [ -x /usr/bin/xcodebuild ] || { echo "Xcode is required for the iOS engine" >&2; exit 2; }
    # Full Xcode: a DEVELOPER_DIR another toolchain set (a Nix Apple SDK, the
    # Command Line Tools) has no iOS SDK.
    if [ -d /Applications/Xcode.app/Contents/Developer ]; then
        export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
    else
        DEVELOPER_DIR="$(/usr/bin/xcode-select -p)"
        export DEVELOPER_DIR
    fi
    unset SDKROOT || true
    export PATH="/usr/bin:/bin:$PATH"
    local cc cxx
    cc="$("$xcrun" -f clang)"
    cxx="$("$xcrun" -f clang++)"
    local slices=()
    for sdk in iphoneos iphonesimulator; do
        local metal=OFF
        # Metal on a device. The simulator runs the CPU build: its Metal is a
        # translation that ggml does not support.
        [ "$sdk" = iphoneos ] && metal=ON
        local dir="$WORK/ios-$sdk"
        local sysroot
        sysroot="$("$xcrun" --sdk "$sdk" --show-sdk-path)"
        cmake_build "$dir" -DSKY_TARGET=ios -DCMAKE_SYSTEM_NAME=iOS \
            -DCMAKE_C_COMPILER="$cc" -DCMAKE_CXX_COMPILER="$cxx" \
            -DCMAKE_OSX_SYSROOT="$sysroot" -DCMAKE_OSX_ARCHITECTURES=arm64 -DCMAKE_OSX_DEPLOYMENT_TARGET=17.0 \
            -DGGML_METAL="$metal" -DGGML_METAL_EMBED_LIBRARY=ON -DGGML_BLAS=OFF -DGGML_NATIVE=OFF
        # One static library per slice: the core and every llama/ggml archive.
        local libs
        libs="$(find "$dir" -name '*.a' | sort)"
        # shellcheck disable=SC2086
        "$xcrun" libtool -static -o "$dir/libskyllama.a" $libs 2>/dev/null
        mkdir -p "$dir/headers"
        cp -f "$HERE/sky_llama.h" "$HERE/module.modulemap" "$dir/headers/"
        slices+=(-library "$dir/libskyllama.a" -headers "$dir/headers")
    done
    rm -rf "$OUT/ios" && mkdir -p "$OUT/ios"
    with_timeout 600 /usr/bin/xcodebuild -create-xcframework "${slices[@]}" -output "$OUT/ios/SkyLlama.xcframework" > "$WORK/xcframework.log" 2>&1 \
        || { tail -20 "$WORK/xcframework.log"; exit 1; }
    notice "$OUT/ios"
    (cd "$OUT" && tar -czf "sky-llama-$BUILD-ios.tar.gz" ios)
)

case "$TARGET" in
    web) build_web ;;
    android) build_android ;;
    ios) build_ios ;;
    all) build_web; build_android; build_ios ;;
esac
(cd "$OUT" && shasum -a 256 sky-llama-$BUILD-*.tar.gz > SHA256SUMS)
cat "$OUT/SHA256SUMS"
