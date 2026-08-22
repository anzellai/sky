# Sky.Spa Todos — native Android shell

The native mobile shell for the [Sky.Spa Todos](..) client. It is a thin
Android `WebView` (`app/src/main/java/dev/sky/spatodos/MainActivity.java`) that
loads the **same** client the web and desktop builds use — the Sky TEA loop
(Model / Msg / update / view) compiled to wasm, served over HTTP by its own
stateless backend. The client and server stay **separate**; only the shell is
native, exactly like `Std.Webview.url` on desktop. One Sky.Spa app spans web,
desktop, and mobile with no per-platform app logic.

`MainActivity` loads `http://10.0.2.2:8951/` — the Android **emulator's** alias
for the host machine's `localhost`, where the backend runs (`../run.sh`,
`TODOS_PORT` default 8951). For a real device or production, change `APP_URL` to
the deployed backend over `https`.

## Run it (emulator)

```bash
# 1. start the backend + build the wasm client on the host
cd ..            # examples/60-spa-todos
./run.sh &       # serves the client + API on :8951

# 2. boot an emulator, build + install the shell
emulator -avd <your_avd> &
cd mobile-android
ANDROID_HOME=~/Library/Android/sdk ./build-apk.sh    # -> build/spa-todos.apk
adb install -r build/spa-todos.apk
adb shell am start -n dev.sky.spatodos/.MainActivity
```

`build-apk.sh` builds directly with the Android SDK tools (aapt2 → javac → d8 →
zipalign → apksigner) — **no Gradle / Android Studio required**. Open the folder
in Android Studio instead if you prefer a Gradle project.
