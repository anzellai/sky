# 75 · AI everywhere: private notes with an on-device assistant

`Std.Ai.Device` in one app. A note's summary is made by a model on the user's
own device when the device can run it and the user agreed to the download, and
by the app's server otherwise, with the same `Provider` code. One `App.app`
builds for the web, the browser client, the Mac, the iPhone and Android.

```bash
sky run src/Main.sky                                   # the web app on http://localhost:8075
sky build --target web:app src/Main.sky                # the browser client
sky test tests/NotesTest.sky                           # the server side, offline
```

## What the app does

- **Notes** (`src/Notes.sky`): a `Std.Db.Store` on `notes.db`. The database is
  set in code (`Notes.database`, the entry's `config`); `SKY_DB_PATH` moves it.
- **The assistant** (`src/Assist.sky`): the `notes` slot is a managed local
  engine on the server (`Local.devModel`, Qwen2.5 0.5B Q4_0) given
  `Endpoint.withDevice (Device.spec Device.Auto |> Device.withContext 2048)`.
  The client asks `Device.provider "notes"`.
- **The consent card**: the page reads `Device.status "notes"`. On a device
  that can run the model it offers the download with its size; `Download`
  runs `Device.prepare`, and the page reads the progress every second while it
  runs. `Remove from this device` runs `Device.evict`.
- **The badge**: each summary appears as the model writes it
  (`Provider.openStream`, then `Provider.readStream` one piece per update) and says, at
  the end, whether it was made on the device or on the server.
- **Help** (`/help`): the server-only `help` slot answers through the server.
- **Self-check** (`/selfcheck`): the device answers the prompts of one eval
  suite (`Device.recordPrompts` over `Eval.prompts`), and the server scores that
  recording and its own slot with the same suite (`Device.replay`,
  `Eval.runAs`), then shows both pass rates.

No setting is in `sky.toml`. The operator overrides the code's defaults with
`SKY_AI_NOTES_PLACEMENT` (`auto`, `device`, `server`),
`SKY_AI_NOTES_DEVICE_MODEL`, `SKY_AI_NOTES_DEVICE_ADAPTER` and the usual
`SKY_AI_NOTES_*` / `SKY_AI_HELP_*` slot variables.

## Before the first build: the device engines

The engines are llama.cpp b11200 with Sky's engine core, one per target.
Install the ones you build for once:

```bash
sky ai provision --dev-model           # the server's engine and the dev model
sky ai provision --device web          # the browser engines (WebGPU and CPU)
sky ai provision --device ios          # SkyLlama.xcframework (device + simulator)
sky ai provision --device android      # libskyllama.so (arm64-v8a, x86_64)
```

`--from <dir>` installs a local build of
`scripts/ai/device/build-device-bundles.sh` instead of the published release.

## Each target

| Target | How a summary is made | Try it |
|---|---|---|
| `web` | on the server (a server-rendered page has no device engine) | `sky run src/Main.sky` |
| `web:app`, Chrome or Edge with a GPU | WebGPU in a Web Worker after the download | `sky build --target web:app src/Main.sky`, run the backend, open it |
| `web:app`, Safari or Firefox | the CPU build in a Web Worker (no JSPI for WebGPU) | the same |
| `web:app`, a private window | on the server (the browser keeps no storage) | the same |
| `desktop:mac` | the managed llama-server with Metal, in the desktop app | `sky build --target desktop:mac src/Main.sky` |
| `mobile:ios` | llama.cpp in the app (Metal on a phone, the CPU on the simulator) | `sky build --target mobile:ios src/Main.sky`, then `xcrun simctl install booted …` |
| `mobile:android` | llama.cpp in the app (the CPU, two threads) | `sky build --target mobile:android src/Main.sky`, then `adb install -r …` |

`--ai-web-engine webgpu|cpu|both|none` picks the browser engines (both, by
default, when they are installed). `--ai-embed notes` puts the model in the
native app, so it never downloads (the app is then 430 MB larger).

The native apps load the backend at `App.withAppUrl` or `SKY_APP_URL` at build
time, else `localhost` / `10.0.2.2` on the build's `PORT`. Start the backend
before the app.

## The privacy promise

A summary made on the device never reaches the server: the page sends no
request for it. The model and the adapter come from the app's own server and
are checked against their SHA-256 before use. No prompt and no answer is
stored on the device, and nothing reports what the device did.
