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
browser's `crypto.getRandomValues`. Everything else is classified as before:
`Crypto.*` (AEAD, random bytes, keyed MACs), `Secret.fromEnv`, and any member
added to these modules later stay on the server until they are listed
(`CLIENT_CRYPTO_MEMBERS` in `rust/crates/project/src/spa_partition.rs`; a test
fails when a new key-holding kernel is not placed).

## What the build refuses

With the opt-in, the build refuses every flow it can see that would move a
device key to the server. A key type is `Kx.SecretKey`, `Sign.SecretKey`,
`Noise.Handshake`, `Noise.Transport`, `Cpace.Pending` or `Secret`, anywhere in a
type (inside a `Maybe`, a `List`, a record, a tuple).

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
`String` field.

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
  `Err Unavailable`; there is no fallback to `localStorage`.
- **What the build cannot see.** A key you turn into a `String` yourself
  (`Kx.secretKeyToBytes` then `Secret.reveal`, or a base64 export) is a
  `String`, and the build cannot tell it from any other text. Do not send such a
  value.

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
- `runtime-go/rt/noise_wasm_interop_test.go`: a Noise IK (BLAKE2s) handshake
  and a transport round trip between the Go wasm build (under Node.js, with the
  client's own `fetch` kernel) and a native Go responder, and a check that keys
  generated in wasm differ.
