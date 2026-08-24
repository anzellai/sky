#!/usr/bin/env bash
# Build + sign the Sky.Spa Todos Android WebView shell APK using the Android SDK
# tools directly (aapt2 -> javac -> d8 -> zipalign -> apksigner) — no Gradle, no
# Android Studio. Requires: ANDROID_HOME (an SDK with build-tools + a platform),
# a JDK (javac), and ~/.android/debug.keystore (created by adb/Studio on first
# use; this script generates one if missing).
#
#   ANDROID_HOME=~/Library/Android/sdk ./build-apk.sh
#
# Output: build/spa-todos.apk. Install with:
#   adb install -r build/spa-todos.apk
#   adb shell am start -n dev.sky.spatodos/.MainActivity
set -euo pipefail
cd "$(dirname "$0")"

: "${ANDROID_HOME:=$HOME/Library/Android/sdk}"
BT="$(ls -d "$ANDROID_HOME"/build-tools/* | sort -V | tail -1)"
PLAT="$(ls -d "$ANDROID_HOME"/platforms/android-* | sort -V | tail -1)/android.jar"
KS="$HOME/.android/debug.keystore"
[ -f "$KS" ] || keytool -genkeypair -keystore "$KS" -storepass android -keypass android \
  -alias androiddebugkey -keyalg RSA -keysize 2048 -validity 10000 \
  -dname "CN=Android Debug,O=Android,C=US"

rm -rf build && mkdir -p build/gen build/classes
"$BT/aapt2" compile --dir app/src/main/res -o build/res.zip
"$BT/aapt2" link -o build/base.apk -I "$PLAT" \
  --manifest app/src/main/AndroidManifest.xml -R build/res.zip --java build/gen \
  --min-sdk-version 24 --target-sdk-version 35 --auto-add-overlay
javac --release 11 -cp "$PLAT" -d build/classes \
  $(find build/gen -name '*.java') $(find app/src/main/java -name '*.java')
"$BT/d8" --lib "$PLAT" --min-api 24 --output build/ $(find build/classes -name '*.class')
( cd build && zip -q base.apk classes.dex )
"$BT/zipalign" -f 4 build/base.apk build/aligned.apk
"$BT/apksigner" sign --ks "$KS" --ks-pass pass:android --key-pass pass:android \
  --min-sdk-version 24 --out build/spa-todos.apk build/aligned.apk
"$BT/apksigner" verify build/spa-todos.apk && echo "OK -> build/spa-todos.apk"
