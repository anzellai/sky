# Keys on the device: `withClientCrypto`

A Sky.Spa app (the `web:app`, `mobile:*`, `desktop:<os>` and `tablet:*`
targets) is split into a wasm client and a Go backend. By default every
`Std.Crypto` function that holds or derives a secret key runs on the
**server**: `Noise`, `Cpace` and `Kdf` as a whole, key generation, signing and
key agreement in `Sign` and `Kx`. That is the safe default, because the client
is code on the user's machine.

Some apps need the opposite. A device that must hold **its own end** of an
end-to-end encrypted session (a phone that pairs with another device, a client
that talks Noise to a server it does not trust with its key) must create and
keep its key on the device. `withClientCrypto` is the explicit opt-in for that.

## Turning it on

On a Std.App entry:

```elm
-- doc-example: skip  (fragment — init/update/view/subscriptions elided)
app =
    App.app { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withNotFound NotFound
        |> App.withClientCrypto
```

On a hand-written Sky.Spa entry:

```elm
-- doc-example: skip  (fragment)
main =
    Spa.app
        (Spa.config { init = init, update = update, view = view, subscriptions = subscriptions }
            |> Spa.withClientCrypto
        )
```

Both builders return the app unchanged at run time. The build reads them from
the typed program: the marker must be reachable from the entry's `main`
(matched by definition, so an import alias works and a function of the same
name does not count, and a marker in dead code does not turn it on). On the
`web` (Sky.Live) and terminal targets there is no client split and
`App.withClientCrypto` does nothing.

## What changes

With the opt-in, these members run in the **wasm client**:

| Module | Members |
|---|---|
| `Std.Crypto.Noise` | every function (`initiator`, `initiatorWith`, `responder`, `responderWith`, `writeMessage`, `readMessage`, `transport`, `encrypt`, `decrypt`, `rekeySend`, `rekeyReceive`, `peer`, `transportPeer`, `handshakeHash`, `isComplete`) |
| `Std.Crypto.Cpace` | `start`, `respond`, `finish`, `messageData` |
| `Std.Crypto.Kx` | `generate`, `publicKey`, `sharedSecret`, secret-key import and export |
| `Std.Crypto.Sign` | `generate`, `sign`, `publicKey`, secret-key import and export |
| `Std.Crypto.Kdf` | `extract`, `expand` |

Keys are generated from Go's `crypto/rand`, which in the wasm client reads the
browser's `crypto.getRandomValues`.

With the opt-in, the `Sky.Core.Crypto` primitives that are pure over a key the
caller holds may run on **either side**: a client arm uses them under a key it
derived with `Kdf`, and a server arm may still use them under a server key.

| `Sky.Core.Crypto` | Members |
|---|---|
| AEAD, explicit nonce | `chacha20Poly1305Seal` / `Open`, `xchacha20Poly1305Seal` / `Open` |
| AEAD, random nonce (a `Task` that draws the nonce from `crypto/rand`) | `xchachaSeal`, `xchachaSealWith`, `aesGcmEncrypt`, `chacha20Encrypt` |
| AEAD, open | `xchachaOpen`, `xchachaOpenWith`, `aesGcmDecrypt`, `chacha20Decrypt` |
| Keyed MACs | `hmacSha256`, `hmacSha512` |
| Password KDFs, RSA signing | `aesKeyFromPassword`, `chachaKeyFromPassword`, `rsaSha256Sign` |

Without the opt-in these stay **server** effects. The reason: by default the
client holds no key material, so the key such a function takes is a server
secret (`Secret.fromEnv`, a constant in the source), and running it in the
client would compile that key, and the code that obtains it, into the wasm
bundle. Hashes, `constantTimeEqual` and `rsaSha256Verify` run in the client
either way. `randomBytes` / `randomToken`, `Secret.fromEnv`, and any member
added to these modules later stay on the server until they are listed
(`CLIENT_CRYPTO_MEMBERS` and `CLIENT_CRYPTO_PURE_MEMBERS` in
`rust/crates/project/src/spa_partition.rs`; a test fails when a new
key-holding kernel is not placed).

## What the build refuses

With the opt-in, the build refuses every flow it can see that would move a
device key to the server. A key type is `Kx.SecretKey`, `Sign.SecretKey`,
`Noise.Handshake`, `Noise.Transport`, `Cpace.Pending` or `Secret`, anywhere in a
type (inside a `Maybe`, a `List`, a record, a tuple). The build decides this on
the type's module, never its bare name: an app type named `Pending` is not
`Cpace.Pending`, and it does not hide the real key from the rule (v0.27.0;
before, a follow-up Msg carrying `Cpace.Pending` was given a codec for the
app's own `Pending`).

| Flow | Result |
|---|---|
| A server branch reads or writes a key field, or a server Msg carries a key | Build error: the key "never crosses between client and server" |
| A codec for a key type on any wire (RPC, follow-up Msg, session projection) | Build error, whatever codec the project declares |
| One `update` branch does a key operation AND a server effect | Build error naming the branch: do the key operation in a client arm and send a Msg whose server arm uses public values only |
| `init`, `withOnNavigate` or `withRequest` reaches a key operation | Build error: these also run on the server for the first paint, so the server would create or hold the key |
| A model field holds a key and is not a top-level `Maybe` (`List Kx.SecretKey`, `{ k : Noise.Transport }`) | Build error: declare it `Maybe K` |
| A model field of type `Maybe K` | Allowed. The first-paint model and the saved model (localStorage) write it as `Nothing`, so neither the page HTML nor the stored model carries the key. It is `Nothing` after a reload. |

The pattern that works: a client arm creates the key and runs the handshake,
the model keeps the handshake or transport in a `Maybe` field, and only public
bytes (a handshake message, a ciphertext, a public key) go to the server in a
`String` field. A relay step is a server arm that forwards the bytes and
returns `model` unchanged, and the Msg its command produces is a client arm
that does the key operation:

```elm
-- doc-example: skip  (fragment — the full app is rust/crates/sky/tests/fixtures/spa-client-crypto-relay)
        SendEcho (Ok hex) ->
            ( model, Cmd.perform (relay "/echo" hex) GotEcho )

        GotEcho (Ok hex) ->
            -- decrypts with model.tr, in the client
```

Each such step is a client-result RPC: the server runs the relay and answers
with its result, and the client runs `GotEcho` on the model it holds. Every
step has that shape, whatever the next client arm does (before v0.27.0 a step
whose client arm ended with `Cmd.none` was run on the server as a chain, and
the build refused it because the chain would hold `tr`). A relay arm that
also writes the model (`{ model | status = "sending" }`) makes that write in
the client when the Msg runs, and its answer carries the result; one that
reads or writes a key field is refused.

Client arms that use the transport are safe while a relay step is in flight:
every Msg's `update` runs exactly once, in arrival order
([overview.md](overview.md), "Msg order and server calls"). A `Seal` clicked
during a relay step runs once, on the transport the model holds, and the
relay's result runs when it arrives. Before v0.27.0 the client re-ran such an
arm on top of the relay's answer and the Noise guard refused the spent state
("this state value was already used"). The relay steps can also overlap: a
long read and a send are separate RPCs in flight together.

A `Maybe` key field needs no type annotation on `init` or `update`: the build
reads its type from the model's `type alias`. The first paint's encoder is
typed with that alias, the field is written `Nothing` there and in the saved
model, and it is set to `Nothing` again after the client decodes either of
them.

## Threat model

- **The server never holds the device's key.** The build enforces this for
  every typed value it can see.
- **Any script in the page can read the key.** It lives in the wasm module's
  memory, which JavaScript in the page can read. An XSS, a compromised
  dependency or a browser extension can therefore take it. Serve the page with
  the strict CSP Sky.Spa supports (`script-src 'self' 'wasm-unsafe-eval'`,
  `SKY_CSP=strict`) and do not render untrusted HTML.
- **Long-term keys belong in secure storage on a native shell.** On
  `mobile:ios`, `mobile:android` and `desktop:<os>`, keep a long-term key with
  `Native.secureSet` / `Native.secureGet` (Keychain, Android Keystore), and put
  it in the model only while it is in use. In a plain browser those calls return
  `Err Unavailable`; there is no fallback to `localStorage` (see "Why a plain
  browser has no secure store" below).
- **What the build cannot see.** A key you turn into a `String` yourself
  (`Kx.secretKeyToBytes` then `Secret.reveal`, or a base64 export) is a
  `String`, and the build cannot tell it from any other text. Do not send such a
  value.

## Why a plain browser has no secure store

`Native.secureSet` / `secureGet` return `Err Unavailable` in a plain browser
(`web:app` without a native shell), and that is a decision, not a gap. The
candidate design was a WebCrypto store: a non-extractable AES-GCM key created
with `crypto.subtle.generateKey`, kept in IndexedDB, wrapping each value. It
was not built (v0.27.0), for these reasons.

- **Script in the page reads the values anyway.** An XSS, a compromised
  dependency or an extension runs as the page, so it can call `secureGet` (or
  `crypto.subtle.decrypt` with the stored key handle) and read every value
  while it runs. A non-extractable key stops it copying the KEY, but the
  values are what it wants, and it gets them in plaintext. Against this
  attacker the store is no better than `localStorage`.
- **The key sits on disk beside the ciphertext.** A browser persists a
  `CryptoKey` in IndexedDB by structured clone, in the same profile directory
  as the ciphertext, and the browsers do not seal IndexedDB with an operating
  system key store. An attacker who can read the profile (malware, a stolen
  unencrypted disk, a backup) reads the key material and decrypts. "Encrypted
  at rest" would be true in name only.
- **The name would promise what the native shells deliver.** On iOS, Android
  and macOS the same call uses the Keychain or the Android Keystore: the key
  lives outside the process, is bound to the device and to its unlock state,
  and is not in the app's files. A web store under the same API would read as
  that guarantee and not give it.

What a web app does instead:

- Keep a long-lived secret (a refresh token, an API key) on the **server**,
  and let the browser hold a session cookie. The Sky.Live and Sky.Spa session
  cookies are `HttpOnly`, so script in the page cannot read them at all: the
  one browser primitive that does resist an XSS.
- Keep a device key the client must hold (see "What changes" above) in the
  model for the session, and re-establish it (pairing, or `Cpace` from a
  password) when the page loads.
- Ship the app with a native shell (`mobile:*`, `desktop:mac`) when the
  device must keep a key across restarts.

## Tests

- `rust/crates/project/src/spa_partition.rs`:
  `client_crypto_policy_moves_only_the_listed_members`,
  `client_crypto_members_cover_the_stdlib`.
- `rust/crates/project/tests/spa_client_crypto.rs`: the verdicts with and
  without the opt-in, a dead marker, and each refusal.
- `rust/crates/sky/tests/spa_split_flow.rs`
  `client_crypto_std_app_builds_and_leaves_keys_out_of_the_first_paint`: a
  Std.App `--target web:app` build (backend and wasm) with the `Maybe` key
  field written as `Nothing` in the first paint and the saved model.
- `rust/crates/project/tests/spa_client_crypto.rs`
  `two_relay_steps_of_the_same_shape_are_both_client_result_rpcs` and
  `a_relay_arm_that_writes_the_key_field_is_still_refused`;
  `spa_split_flow.rs`
  `a_device_key_field_with_a_server_branch_builds_and_paints_nothing`.
- `scripts/spa-client-crypto-e2e.sh`: the relay fixture in a browser against a
  Go Noise responder (`runtime-go/rt/noisewasm/responder`): the wasm client
  completes the handshake and a transport round trip through two relay steps,
  and each relay request carries only hex.
- `runtime-go/rt/noise_wasm_interop_test.go` with `noisewasm/aead_js_test.go`:
  under a key both sides derive with `Kdf`, the wasm build's random-nonce seal
  (two seals differ), explicit-nonce AEAD and HMAC agree with the native build.
- `spa_split_flow.rs` `client_crypto_seals_and_macs_in_the_client_under_the_opt_in`:
  a client arm that seals under a `Kdf` key and a MAC arm are client branches
  with the opt-in, server branches without it.
- `runtime-go/rt/noise_wasm_interop_test.go`: a Noise IK (BLAKE2s) handshake
  and a transport round trip between the Go wasm build (under Node.js, with the
  client's own `fetch` kernel) and a native Go responder, and a check that keys
  generated in wasm differ.
