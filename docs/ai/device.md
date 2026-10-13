# On-device models: `Std.Ai.Device`

*(v0.28.0)* A model slot can run on the user's own device: in the browser, in
the iPhone and Android apps, and in the Mac app. The server keeps the slot and
its policy. The client asks `Device.provider "<slot>"`, an ordinary
`Std.Ai.Provider`, and each call runs on the device when the device holds the
model and can run it, and on the server's slot otherwise. The app code is the
same on every target, and one eval suite measures both places.

Every target runs the same engine: llama.cpp `b11200` with Sky's engine core
(`scripts/ai/device/sky_llama.cpp`), so a device loads the same GGUF model and
the same GGUF LoRA adapter the server serves (a `Std.Ai.Train` adapter
included), with the same chat template and the same sampler.

The live API is `sky doc Std.Ai.Device`. The design is
[`../design/std-ai-lifecycle.md`](../design/std-ai-lifecycle.md) §14 P5, and
`examples/75-ai-everywhere` is the app that uses all of it.

## Quick start

The server owns the slot. `Endpoint.withDevice` gives it a device spec:

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Log exposing (println)
import Std.Ai.Provider as Provider exposing (Provider)
import Std.Ai.Endpoint as Endpoint exposing (Endpoint)
import Std.Ai.Local as Local
import Std.Ai.Device as Device


notesSlot : Endpoint
notesSlot =
    Endpoint.endpoint "notes"
        |> Endpoint.withLocal (Local.model Local.devModel)
        |> Endpoint.withDevice (Device.spec Device.Auto |> Device.withContext 2048)


assistant : Provider
assistant =
    Device.provider "notes"


main =
    Provider.chat assistant [ Provider.user "Summarise: train to Paris on Monday." ]
        |> Task.andThen (\r -> println r.content)
        |> Task.run
```

This program runs on the server, so `assistant` is the `notes` slot itself. In
a Sky.Spa client (`web:app`, `desktop:mac`, `mobile:ios`, `mobile:android`)
the same `Device.provider "notes"` runs in the client, and the device decides
per call.

The build checks the pairing: a `Device.provider "x"` needs an
`Endpoint.endpoint "x"` binding with `Endpoint.withDevice` in a module the
program imports, the binding must be top level with no parameters, and both
name the slot with a literal (or a top-level `String` constant). Each of these
is a build error, because the server registers the slot when it starts.

## Placement

The spec's placement is required. It says where a call may run:

| Placement | The device can run it | It cannot |
|---|---|---|
| `Device.Auto` | on the device | on the server |
| `Device.DeviceOnly` | on the device | a typed failure; the server never answers |
| `Device.ServerOnly` | on the server | on the server |

`DeviceOnly` has no fallback route on the server at all, so a slot that must
keep its prompts off the server cannot leak them to it.

## Configuration: code holds the default, the operator overrides it

No setting is in `sky.toml`. The spec's builders hold the defaults, and the
operator overrides them per slot:

| Builder | Variable | Default |
|---|---|---|
| `Device.spec Auto/DeviceOnly/ServerOnly` | `<PREFIX>_AI_<SLOT>_PLACEMENT` (`auto`, `device`, `server`) | required in code |
| `Device.withModel ref` | `<PREFIX>_AI_<SLOT>_DEVICE_MODEL` | the slot's local model (`Endpoint.withLocal`) |
| `Device.withAdapter ref` | `<PREFIX>_AI_<SLOT>_DEVICE_ADAPTER` | the slot's adapter |
| `Device.withContext n` | | 2048 tokens |
| `Device.withMaxDownload bytes` | | 1 GiB |
| `Device.withBackends [ Native, WebGpu, WasmCpu ]` | | all three, in that order |
| `Device.withMetered True` | | `False`: no download on a metered connection |

A model is a `Std.Ai.Local` reference with its SHA-256 (`Local.reference
Local.devModel`). An adapter is `registry:<name>@<alias>` (the slot needs
`Endpoint.fromRegistry`) or a pinned file reference.

The build options are flags:

| Flag | What it does |
|---|---|
| `sky ai provision --device web\|ios\|android\|all [--from <dir\|archive>]` | install the engines the builds stage (below) |
| `sky build --ai-web-engine webgpu\|cpu\|both\|none` | which browser engines a client build stages; default `both` when installed |
| `sky build --ai-embed <slot>` | a native app (`desktop:mac`, `mobile:ios`, `mobile:android`) carries the slot's model and adapter, so it never downloads them |

## Consent, status and the download

Nothing downloads on its own. A device that does not hold the model answers
from the server (`Auto`) or fails with `NotProvisioned` (`DeviceOnly`) until
the app calls `Device.prepare`, which the app runs after the user agreed:

```elm
-- doc-example: skip  (fragment of examples/75-ai-everywhere)
Device.status "notes"      -- Ready, NotDownloaded { bytes }, Downloading, ServerSide reason, Unsupported failure
Device.prepare "notes"     -- download, check, load; then Ready
Device.evict "notes"       -- remove the files and unload
```

`NotDownloaded` carries the size to show in the consent card. While
`prepare` runs, `status` reports `Downloading { doneBytes, totalBytes }`;
poll it with `Sub.every 1000`. A download is 64 MiB Range requests. Each chunk
is checked against the server's chunk list and the whole file against the
SHA-256 in the offer, before the file is marked ready. An interrupted browser
download resumes from the last whole chunk.

| Where | The device's store |
|---|---|
| a browser | the origin-private file system (OPFS), with `navigator.storage.persist()` requested |
| iOS | Application Support/sky-ai, excluded from backups |
| Android | the app's files directory, sky-ai/ |
| macOS | `<AI home>/device/files` (a model already pulled by `Std.Ai.Local` is used in place) |

A browser that refuses storage (a private window) reports no room, so `Auto`
answers from the server and `DeviceOnly` fails with `StorageDenied`.

## How each call is decided

`Device.decide` is the whole policy as a pure function of the device's
capabilities, the server's offer and the prompt's length. The first rule that
applies wins:

1. A `ServerOnly` placement (the code's or the operator's), or code that runs
   on the server: the server.
2. An offer the server refused (an adapter not cleared for devices): the
   server for `Auto`, `NotPromotable` for `DeviceOnly`.
3. The first backend of the offer's list that the device has, that has not
   failed this session, and that can hold the model and the adapter. None:
   the server for `Auto`, `DeviceUnsupported` for `DeviceOnly`.
4. Files not stored need 1.1 times their size free: else the server for
   `Auto`, `StorageDenied` for `DeviceOnly`.
5. Files not stored: `NeedsDownload` (the server answers this call for `Auto`;
   `DeviceOnly` fails with `NotProvisioned`).
6. A prompt longer than three quarters of the device's context: the server
   for `Auto`, `InvalidSetting` for `DeviceOnly`.
7. Otherwise the device.

## Backends

| Backend | Where | Notes |
|---|---|---|
| `Native` | the iOS and Android apps, the Mac app | llama.cpp in the app: Metal on an iPhone, the CPU on the iOS simulator; the CPU with two threads on Android (16 KB-aligned, uncompressed `libskyllama.so` for arm64-v8a and x86_64); the managed `llama-server` with Metal on a Mac |
| `WebGpu` | Chrome, Edge | llama.cpp's WebGPU backend in a Web Worker. It needs JavaScript Promise Integration and a GPU with `shader-f16`, so Safari and Firefox never get it. A WebGPU engine that registers no GPU device fails with `DeviceUnsupported`; it never runs on the CPU in silence, and the next call uses the next backend |
| `WasmCpu` | every browser with OPFS | the CPU build of the same engine in a Web Worker; models up to 1 GiB |

The worker and the engines are same-origin files with content-hashed names
(`sky-ai-worker.<hash>.js`, `sky-ai-webgpu.<hash>.{mjs,wasm}`,
`sky-ai-cpu.<hash>.{mjs,wasm}`); the build writes the page's `sky-ai-engine`
meta that names them. Nothing evaluates a string, so the strict policy
(`SKY_CSP=strict`: `script-src 'self' 'wasm-unsafe-eval'`) runs them with no
violation.

Measured on an Apple M-series Mac (16 GB) with the development model (Qwen2.5
0.5B, Q4_0, 430 MB) and a 24-step `Std.Ai.Train` MLX adapter, asking one short
question: Chromium WebGPU (a Metal adapter) answered in 0.45 s, Chromium CPU in
5.5 s, WebKit CPU in 6.1 s. A local download of the model took 7 s in
Chromium and 13 s in WebKit. The iOS 27 simulator (the model downloaded from
the backend) and the Android 15 arm64 emulator (the model carried in the app
with `--ai-embed`, 28 s from launch to the answer) answered "The capital of
France is Paris." on the device. The Mac app ran the synthetic model on its
managed engine. The WebGPU answer with the adapter matched the server's word
for word. In `examples/75-ai-everywhere` a note's summary took 10.1 s on the
CPU build and 0.8 s on WebGPU, and the self-check (three prompts on the
device, scored on the server) took 27.1 s and 2.8 s, with both pass rates at
100%. Streamed (`Provider.stream`), the development model's answer to the
probe's question arrived in 7 pieces that joined to the same text in
Chromium (CPU and WebGPU), in WebKit, from the server, and in the Android
emulator with the model carried in the app.

## Streaming

`Provider.stream` and `Provider.read` (or `Provider.chatStream` with a
callback) work on `Device.provider` like on any provider, and the decision is
the same as for a call. On the device the engine hands over each piece of
text as it writes it: the browser worker, the iOS and Android apps step the
engine core (`sky_llama_chat_begin` / `sky_llama_chat_next`, whole UTF-8
pieces), and the Mac app reads its managed engine's event stream. When the
decision is the server, the client reads `POST /_sky/ai/device/<slot>/stream`.
On the server itself (Sky.Live, tests) it is the slot's own stream. A piece
read on the device sends nothing to the server.

## The server's routes

A server with a device slot answers:

| Route | What |
|---|---|
| `GET /_sky/ai/device/<slot>` | the slot's offer (placement, model and adapter with SHA-256, size and URL, context, backends); `ETag`, revalidated every 30 s, so an alias move reaches devices |
| `GET /_sky/ai/blob/<sha256>` | an offered file, with Range requests; only the digests a current offer names (never a path) |
| `GET /_sky/ai/blob/<sha256>.chunks.json` | its 64 MiB chunk digests |
| `POST /_sky/ai/device/<slot>/call` | the fallback call: same origin, `application/json`, at most 64 KiB, 30 calls a minute per session; absent for `DeviceOnly` |
| `POST /_sky/ai/device/<slot>/stream` | the streaming fallback call (`Provider.stream` in a client the decision sends to the server): the same rules and rate as `/call`, answered as server-sent events, one piece per event, a failure as a typed event; absent for `DeviceOnly` |

## Privacy

- A call that runs on the device sends nothing to the server: the page makes
  no request for it.
- No prompt and no answer is stored on the device, and the device reports no
  telemetry about its calls.
- An adapter's weights can carry what it was trained on, so a registry
  adapter runs on devices only after `Registry.allowOnDevice registry version
  reason`. That needs a GGUF version trained on a dataset version in the same
  registry that was scrubbed (`Dataset.scrub`), records the reason on the
  version and logs an `allow-on-device` event. A slot whose adapter is not
  cleared offers it refused: `Auto` answers from the server, `DeviceOnly`
  fails with `NotPromotable`.

## Failures

Every failure is a typed `Std.Ai.Failure`:

| Failure | When |
|---|---|
| `DeviceUnsupported { backend, reason }` (`Unavailable`) | no backend can run the model (`DeviceOnly`), or a WebGPU engine found no GPU device |
| `StorageDenied { needBytes, availableBytes }` (`PermissionDenied`) | the device has no room, or the browser keeps no storage (`DeviceOnly`) |
| `NotProvisioned` | a `DeviceOnly` slot is not downloaded yet |
| `NotPromotable` | the adapter is not cleared for devices (`DeviceOnly`) |
| `ChecksumMismatch` | a downloaded chunk or file does not match its digest |
| `InvalidSetting` | the prompt does not fit the device's context (`DeviceOnly`) |
| `EngineFailed` | the engine could not load the model or the adapter |

## One eval suite on the device and the server

`Eval.prompts suite` lists every request a run of the suite sends.
`Device.record label provider prompts` asks a provider each of them on the
device and keeps the answers; send the `Recording` to the server, and score it
there with the same suite:

```elm
-- doc-example: skip  (fragment of examples/75-ai-everywhere/src/Suites.sky)
Eval.runAs rec.label db (Device.replay rec) summaries
```

`Device.replay` answers each recorded request as recorded, so the device and
the server's slot are measured by the same scorers and thresholds. A replay
answers by the messages alone, so record a suite with one sample per case.

## Engines and their provenance

The engines are built from the pinned llama.cpp source (`b11200`, the source
archive's SHA-256 is in `rust/crates/sky/src/ai_device.rs`) with emsdk 6.0.12
and Dawn's emdawnwebgpu v20260423.175430 (web), the Android NDK 27.0.12077973
(android) and Xcode (ios), by `scripts/ai/device/build-device-bundles.sh`. The
`ai-device-bundles` workflow publishes them as the release `sky-llama-b11200`,
and `sky ai provision --device <target>` installs a published archive after
checking its pinned SHA-256. `--from <dir|archive>` installs a local build of
the same script. The bundles carry their licences (`NOTICE.txt`; llama.cpp
and ggml MIT, nlohmann/json MIT, Emscripten MIT and NCSA, Dawn BSD 3-Clause).

## Testing

Under `sky test` a server-side `Device.provider` is the slot itself, so its
calls are answered by `tests/mocks/` like any slot. The device flows are
covered by `tests/Std/Ai/DeviceTest.sky` (the decision, the wire values,
record and replay), `runtime-go/rt/ai_device_*_test.go` (the routes, the
worker's digests and its refusal to run WebGPU on the CPU, the Mac bridge),
`scripts/ai-device-e2e.sh` (Chromium and WebKit under the strict policy) and
`rust/crates/sky/tests/ai_device_native_flow.rs` (the iOS simulator, the
Android emulator and the Mac app; the probe also streams its answer and
checks it arrived in more than one piece). Streaming is covered by
`runtime-go/rt/ai_stream_test.go` (the `ai-stream-pieces` gate) and by
`scripts/ai/device/test-engine-core.sh`, which builds the engine core for the
build machine's CPU and checks that the streamed answer equals the one-call
answer in whole UTF-8 pieces. `go run ./cmd/sky-tiny-gguf <dir>` (in
`runtime-go/`) writes a 0.4 MB synthetic Qwen2 model and adapter that every
engine loads, so the flows run without the 430 MB model.
