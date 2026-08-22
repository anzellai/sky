#!/usr/bin/env bash
# Build the Sky.Spa Todos iOS app for the SIMULATOR directly with swiftc — no
# .xcodeproj needed. Requires full Xcode (DEVELOPER_DIR pointed at Xcode.app).
#   DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer ./build-app.sh
# Output: build/SkySpaTodos.app  (install via: xcrun simctl install booted build/SkySpaTodos.app)
set -euo pipefail
cd "$(dirname "$0")"
: "${DEVELOPER_DIR:=/Applications/Xcode.app/Contents/Developer}"; export DEVELOPER_DIR; unset SDKROOT || true
SDK=$(xcrun --sdk iphonesimulator --show-sdk-path)
rm -rf build && mkdir -p build/SkySpaTodos.app
xcrun --sdk iphonesimulator swiftc -sdk "$SDK" -target arm64-apple-ios17.0-simulator \
  -parse-as-library SkySpaTodos/SkySpaTodosApp.swift SkySpaTodos/WebView.swift \
  -o build/SkySpaTodos.app/SkySpaTodos
cp SkySpaTodos/Info.plist build/SkySpaTodos.app/Info.plist
echo "OK -> build/SkySpaTodos.app"
