# Standard library reference

> **v0.15.x state.** Layer 3 stdlib complete: every kernel module
> surfaced as Sky source under
> `sky-stdlib/{Sky/Core,Std,Sky/Http}/*.sky`. Browse the full surface
> with `sky doc --serve` (HTTP server with type-signature search,
> Markdown rendering, in-module filter), or `sky doc <Module>` in the
> terminal. Fully-typed Go output; whole-program DCE prunes unused
> code + FFI bindings; auto-TCO for tail-recursive functions.
> v0.15 adds **type-directed lowering** end-to-end (lambdas, record
> fields, list literals) and **Go generics on parametric record
> aliases** (`type alias Cfg msg = { ... }` compiles to
> `Cfg_R[msg any]` so callback shapes stay typed across the FFI
> boundary).

Sky's standard library is **batteries-included** — one canonical module
per concern, no plugin ecosystem, no `npm install` for crypto. This
page is the complete user-facing reference.

> Each kernel module is reachable via its bare name. `import Log`
> works the same as `import Std.Log as Log`. The long `Sky.Core.X` /
> `Std.X` paths are kept for cross-language familiarity, but you can
> usually drop them.

**Conventions you'll see throughout this page:**

- **Pure** functions return bare values (`a`) — referentially transparent, deterministic.
- **Fallible-pure** functions return `Result Error a` or `Maybe a` — pure CPU work that can fail on malformed input.
- **Effects** return `Task Error a` — anything that touches the outside world (clock, env, stdout, disk, network, DB, entropy).
- **Default-supplied helpers** stay bare even when the underlying op could fail — the default plugs the failure case at the call site.

See the [Effect Boundary doctrine](../CLAUDE.md#effect-boundary-task-everywhere-v0100) for the full reasoning.

---

## Pure modules (no I/O, no Task wrap)

### `Basics` — auto-imported essentials

Implicitly available everywhere via `Sky.Core.Prelude exposing (..)`. Nothing to import.

| Function | Type | Notes |
|---|---|---|
| `identity` | `a -> a` | The identity function |
| `always` | `a -> b -> a` | Const; ignores second arg |
| `not` | `Bool -> Bool` | Logical not |
| `toString` | `a -> String` | Debug-formatted string of any value |
| `modBy` | `Int -> Int -> Int` | Math modulo (divisor-first argument order, matches Elm) |
| `clamp` | `comparable -> comparable -> comparable -> comparable` | Constrain to range |
| `fst`, `snd` | `(a, b) -> a` / `(a, b) -> b` | Tuple accessors |
| `compare` | `comparable -> comparable -> Order` | LT / EQ / GT |
| `negate`, `abs`, `sqrt` | `number -> number` | Math basics |
| `min`, `max` | `comparable -> comparable -> comparable` | Pick smaller / larger |

### `String` — text manipulation

```elm
import Sky.Core.String as String

main =
    println (String.toUpper "hello")          -- "HELLO"
        ++ println (String.fromInt 42)        -- "42"
        ++ println (String.split "," "a,b,c") -- ["a","b","c"]
```

All 33 entries: `length`, `isEmpty`, `reverse`, `append`, `concat`, `split`, `join`, `replace`, `slice`, `contains`, `startsWith`, `endsWith`, `toInt`, `fromInt`, `toFloat`, `fromFloat`, `toUpper`, `toLower`, `trim`, `trimStart`, `trimEnd`, `repeat`, `padLeft`, `padRight`, `lines`, `words`, `fromChar`, `toList`, `fromList`, `casefold`, `equalFold`, `isEmail`, `isUrl`.

### `List` — sequences

```elm
import Sky.Core.List as List

doubled = List.map (\n -> n * 2) [ 1, 2, 3 ]              -- [2, 4, 6]
sum     = List.foldl (\n acc -> n + acc) 0 [ 1, 2, 3 ]    -- 6
evens   = List.filter (\n -> modBy 2 n == 0) [ 1, 2, 3, 4 ] -- [2, 4]
```

`map`, `filter`, `foldl`, `foldr`, `length`, `head`, `tail`, `take`, `drop`, `append`, `concat`, `concatMap`, `reverse`, `member`, `any`, `all`, `range`, `zip`, `isEmpty`, `indexedMap`, `find`, `cons`.

> v0.17 closed Limitation #8 — all 13 list ops in scope now run
> on constant Go stack. `foldl` / `find` / `any` / `all` /
> `member` / `drop` / `reverse` plus `length` / `range` / `zip` /
> `concatMap` / `indexedMap` are auto-TCO'd (tail-recursive
> helper compiled to `for { ... continue }`). `map` / `filter` /
> `foldr` / `concat` / `take` / `append` / `Maybe.combine` /
> `Result.combine` were CPS-rewritten in v0.17 to delegate
> through the same constant-stack form. Million-entry lists are
> safe across the surface.

### `Dict` — key-value maps

```elm
import Sky.Core.Dict as Dict

prefs = Dict.fromList [ ("theme", "dark"), ("lang", "en") ]
theme = Dict.get "theme" prefs   -- Just "dark"
```

`empty`, `insert`, `get`, `remove`, `member`, `keys`, `values`, `toList`, `fromList`, `map`, `foldl`, `union`, `size`, `isEmpty`.

> **Key types.** The runtime representation is `map[string]V` regardless of the Sky-level key type, so keys are encoded to strings on the way in. Lookup (`get` / `member` / `insert` / `remove`) encodes the probe the same way and works for any key type; the operations that hand the key back — `toList`, `keys`, `values`, `foldl`, `map` — decode it to its Sky type, and `String`, `Int`, `Float`, `Char` and `Bool` decode. Enumeration is ordered by the decoded key, so a `Dict Int v` visits 9 before 10.
>
> The encoded key carries its own type tag, so the decode works with no type information from the call site — a helper written over `Dict k v`, where the compiler has erased the key, hands back Ints from a `Dict Int v` just as a `Dict Int v`-typed call site does. A `Dict String v` is encoded verbatim, so the shape that crosses into JSON objects, `Std.Db` rows and HTTP headers keeps exactly the keys you gave it.
>
> Composite keys (tuple, list, record, custom type) do **not** decode — their stringification is not reversible; see [`KNOWN_LIMITATIONS.md`](KNOWN_LIMITATIONS.md).

### `Set` — unique-element collections

`empty`, `insert`, `remove`, `member`, `union`, `diff`, `intersect`, `fromList`, `toList`, `size`.

### `Maybe` — optional values

```elm
import Sky.Core.Maybe as Maybe

name : String
name = Maybe.withDefault "Anonymous" maybeName
```

`withDefault`, `map`, `andThen`, `map2`, `map3`, `map4`, `map5`, `andMap`, `combine`, `isJust`, `isNothing`.

### `Result` — fallible computations

```elm
import Sky.Core.Result as Result

id = 
    case fallibleComputation of                                       
        Ok result ->                                                    
            println result                                              
                                                                          
        Err e ->                                                    
            println ("computation failed: " ++ Error.toString e) 
```

`withDefault`, `map`, `andThen`, `mapError`, `map2`, `map3`, `map4`, `map5`, `andMap`, `combine`, `toMaybe`.

`Result.toMaybe : Result e a -> Maybe a` turns `Ok a` into `Just a` and `Err _` into `Nothing`. It drops the error, so use it only where the reason for a failure does not matter. It works with or without `import Sky.Core.Result`.

The `Result → Task` bridges live on `Task` (`Task.fromResult` / `Task.andThenResult`) — see [Result/Task bridges](../CLAUDE.md#resulttask-bridges).

### `Tuple` — pairs (`Sky.Core.Tuple`)

```elm
import Sky.Core.Tuple as Tuple

labelled = List.map (Tuple.pair "k") [ 1, 2 ]          -- [ ( "k", 1 ), ( "k", 2 ) ]
shown = Tuple.mapBoth String.toUpper negate ( "a", 1 )  -- ( "A", -1 )
```

| Function | Type |
|---|---|
| `Tuple.pair` | `a -> b -> ( a, b )` |
| `Tuple.first` | `( a, b ) -> a` |
| `Tuple.second` | `( a, b ) -> b` |
| `Tuple.mapFirst` | `(a -> x) -> ( a, b ) -> ( x, b )` |
| `Tuple.mapSecond` | `(b -> y) -> ( a, b ) -> ( a, y )` |
| `Tuple.mapBoth` | `(a -> x) -> (b -> y) -> ( a, b ) -> ( x, y )` |

Pure Sky, no runtime kernel, so it needs the `import`. `fst` / `snd` from `Basics` stay available and are the same as `Tuple.first` / `Tuple.second`.

### `Math` — numerical functions

`sqrt`, `pow`, `abs`, `floor`, `ceil`, `round`, `sin`, `cos`, `tan`, `pi`, `e`, `log`, `min`, `max`.

### `Regex` — pattern matching

```elm
import Sky.Core.Regex as Regex

match : Bool
match = Regex.match "^[a-z]+$" "hello"   -- True
```

`match`, `find`, `findAll`, `replace`, `split`.

### `Char` — character predicates

`isUpper`, `isLower`, `isDigit`, `isAlpha`, `toUpper`, `toLower`.

### `Path` — file path manipulation

`base`, `dir`, `ext`, `isAbsolute`, `join`, `safeJoin`. (`Sky.Ffi` is stdlib-only, `[E1011]`: for Go's full `path/filepath` API, `sky add path/filepath` and call its bindings, which return `Result Error a`.)

### `Crypto` — hashes, MAC, signatures, entropy

```elm
import Sky.Core.Crypto as Crypto

digest = Crypto.sha256 "hello"   -- hex string
hmac   = Crypto.hmacSha256 "secret" "message"
```

| Function | Type | Notes |
|---|---|---|
| `Crypto.sha256` | `String -> String` | Hex digest |
| `Crypto.sha512` | `String -> String` | Hex digest |
| `Crypto.sha1` | `String -> String` | Hex digest — interop only (git ids, legacy webhook signatures) |
| `Crypto.md5` | `String -> String` | Hex digest (legacy support only) |
| `Crypto.hmacSha256` | `String -> String -> String` | Hex HMAC-SHA256 |
| `Crypto.hmacSha512` | `String -> String -> String` | Hex HMAC-SHA512 |
| `Crypto.rsaSha256Sign` | `String -> String -> Result Error String` | RSASSA-PKCS1-v1_5 over SHA-256 ("RS256"); (PEM private key, message) → standard-base64 signature |
| `Crypto.rsaSha256Verify` | `String -> String -> String -> Bool` | (PEM public key, message, base64 signature) → valid? |
| `Crypto.constantTimeEqual` | `String -> String -> Bool` | Side-channel safe comparison |
| `Crypto.randomBytes` | `Int -> Task Error String` | OS entropy: `n` bytes (1..1024), returned **hex-encoded** (`2 × n` characters) |
| `Crypto.randomToken` | `Int -> Task Error String` | OS entropy → URL-safe-base64 string of given byte length |
| `Crypto.xchachaSeal` | `Secret -> String -> Task Error String` | **The recommended AEAD.** XChaCha20-Poly1305 with a random 24-byte nonce; output `base64(nonce \|\| ct \|\| tag)` |
| `Crypto.xchachaSealWith` | `Secret -> String -> String -> Task Error String` | As `xchachaSeal`, and authenticates associated data (key, AD, plaintext) |
| `Crypto.xchachaOpen` | `Secret -> String -> Result Error String` | Inverse of `xchachaSeal`. Err on a wrong key or a tampered value |
| `Crypto.xchachaOpenWith` | `Secret -> String -> String -> Result Error String` | Inverse of `xchachaSealWith`. Err when the associated data differs |
| `Crypto.aesGcmEncrypt` | `Secret -> String -> Task Error String` | AES-256-GCM AEAD, random 12-byte nonce; output `base64(nonce \|\| ct \|\| tag)`. A `Task` since v0.27.0 |
| `Crypto.aesGcmDecrypt` | `Secret -> String -> Result Error String` | Inverse of `aesGcmEncrypt`. Err on tag/key mismatch |
| `Crypto.chacha20Encrypt` | `Secret -> String -> Task Error String` | ChaCha20-Poly1305 AEAD, random 12-byte nonce. A `Task` since v0.27.0 |
| `Crypto.chacha20Decrypt` | `Secret -> String -> Result Error String` | Inverse of `chacha20Encrypt` |
| `Crypto.chacha20Poly1305Seal` | `Secret -> String -> String -> String -> Result Error String` | IETF ChaCha20-Poly1305 (RFC 8439) with a **caller-supplied** 12-byte nonce: (key, nonce, associated data, plaintext) → raw `ct \|\| tag`. Pure and deterministic |
| `Crypto.chacha20Poly1305Open` | `Secret -> String -> String -> String -> Result Error String` | Inverse of `chacha20Poly1305Seal`: (key, nonce, associated data, `ct \|\| tag`) → plaintext |
| `Crypto.xchacha20Poly1305Seal` | `Secret -> String -> String -> String -> Result Error String` | XChaCha20-Poly1305 with a **caller-supplied** 24-byte nonce; same shape as `chacha20Poly1305Seal` |
| `Crypto.xchacha20Poly1305Open` | `Secret -> String -> String -> String -> Result Error String` | Inverse of `xchacha20Poly1305Seal` |
| `Crypto.aesKeyFromPassword` | `Secret -> String -> Secret` | PBKDF2-HMAC-SHA256 100k iter → 32-byte key (a `Secret`) for any AEAD above |
| `Crypto.chachaKeyFromPassword` | `Secret -> String -> Secret` | Same derivation, named for ChaCha |

#### Which AEAD, which key, which effect

- **Use `xchachaSeal` / `xchachaOpen`.** Its 24-byte random nonce never
  repeats in practice, so one key can seal any number of messages.
  `aesGcmEncrypt` and `chacha20Encrypt` use a 12-byte random nonce: keep them
  for interoperability and rotate the key well before 2^32 messages.
- **An explicit nonce is for a fixed protocol, not for new designs.**
  `chacha20Poly1305Seal` / `xchacha20Poly1305Seal` take the nonce from you,
  so they are deterministic: the same key, nonce, associated data and
  plaintext always give the same bytes. **Never use one nonce twice with the
  same key**: a repeated nonce reveals the XOR of the two plaintexts and lets
  an attacker forge tags, so it breaks both confidentiality and authenticity.
  Use them when a protocol fixes the nonce (a message counter, a
  transcript-derived nonce, a published test vector); otherwise use
  `xchachaSeal`. Every argument is raw bytes (`Encoding.hexDecode` /
  `Bytes.fromBase64` for text forms), the output is `ct || tag` without the
  nonce, and a key, nonce or input of the wrong length is `Err InvalidInput`,
  as is a failed authentication.
- **Keys are `Secret`s, 32 bytes.** From a password: `Crypto.aesKeyFromPassword`
  (PBKDF2). From key material (an X25519 shared secret, a master key):
  `Kdf.derive` (HKDF). From configuration: `Secret.fromEnv`.
- **Effects.** Anything that draws randomness is a `Task`: every seal and
  encrypt, `randomBytes`, `randomToken`, and key generation. Open, decrypt,
  hashing, MACs, key derivation, signing and verification are pure.
- **v0.27.0 migration.** `aesGcmEncrypt` and `chacha20Encrypt` became
  `Task Error String` (they draw a nonce). In a `Task` chain use them
  directly; where a `Result` is needed, `Task.run (Crypto.aesGcmEncrypt key pt)`.

### `Std.Crypto.Sign`, `Std.Crypto.Kx`, `Std.Crypto.Kdf` — signatures, key agreement, key derivation

```elm
import Std.Crypto.Sign as Sign
import Std.Crypto.Kx as Kx
import Std.Crypto.Kdf as Kdf

-- Ed25519: generate once, keep the key in an environment variable.
signingKey = Sign.secretKeyFromBase64 (Secret.fromEnv "SIGNING_KEY")   -- Result Error Sign.SecretKey
signature  = Sign.sign secretKey "invoice #42"                         -- 64 raw bytes
valid      = Sign.verify publicKey "invoice #42" signature             -- Bool

-- X25519 then HKDF: a shared key for xchachaSeal.
sessionKey =
    Kx.sharedSecret mySecret theirPublic
        |> Result.andThen (Kdf.derive salt "my-app v1 session" 32)
```

| Function | Type | Notes |
|---|---|---|
| `Sign.generate` | `Task Error Sign.SecretKey` | Fresh Ed25519 key |
| `Sign.publicKey` | `Sign.SecretKey -> Sign.PublicKey` | |
| `Sign.sign` | `Sign.SecretKey -> String -> String` | RFC 8032 Ed25519, 64-byte signature (pure: Ed25519 is deterministic) |
| `Sign.verify` | `Sign.PublicKey -> String -> String -> Bool` | `False` on any failure, including a wrong-length signature |
| `Sign.secretKeyFromBytes` / `secretKeyFromBase64` | `Secret -> Result Error Sign.SecretKey` | 32-byte seed (raw / standard base64); Err on a wrong length |
| `Sign.secretKeyToBytes` / `secretKeyToBase64` | `Sign.SecretKey -> Secret` | Export; still a `Secret` |
| `Sign.publicKeyFromBytes` / `publicKeyFromBase64` | `String -> Result Error Sign.PublicKey` | Err on a wrong length or a non-curve point |
| `Sign.publicKeyToBytes` / `publicKeyToBase64` | `Sign.PublicKey -> String` | |
| `Kx.generate` | `Task Error Kx.SecretKey` | Fresh X25519 key |
| `Kx.publicKey` | `Kx.SecretKey -> Kx.PublicKey` | |
| `Kx.sharedSecret` | `Kx.SecretKey -> Kx.PublicKey -> Result Error Secret` | RFC 7748; **Err on a low-order peer key** (all-zero result) |
| `Kx.secretKeyFrom…` / `secretKeyTo…` / `publicKeyFrom…` / `publicKeyTo…` | as `Sign` | Same import / export shapes |
| `Kdf.extract` | `String -> Secret -> Secret` | HKDF-Extract (salt, input key material) |
| `Kdf.expand` | `Secret -> String -> Int -> Result Error Secret` | HKDF-Expand (key, info, length); Err unless `1 ≤ length ≤ 8160` |
| `Kdf.derive` | `String -> String -> Int -> Secret -> Result Error Secret` | Extract then expand |

`Sign.SecretKey` and `Kx.SecretKey` are opaque: they print as
`[REDACTED]` in every `toString`, log and JSON path, a Sky.Live session store
refuses to save them, and the raw bytes leave only as a `Secret`
(`secretKeyToBytes`), so reading them still needs the greppable
`Secret.reveal`. Public keys print as base64.

### `Std.Crypto.Noise` — authenticated encrypted sessions

`Noise_IK_25519_ChaChaPoly_SHA256`: two messages set up a mutually
authenticated channel when the initiator already knows the responder's static
public key. `Noise.initiator` / `Noise.responder` (Tasks: they draw the
ephemeral key) give a `Handshake`; `writeMessage` / `readMessage` step it;
`peer` shows the initiator's static key to the responder after message 1;
`transport` turns a complete handshake into a `Transport` with `encrypt`,
`decrypt`, `rekeySend`, `rekeyReceive` and `handshakeHash`. Every step returns
the next state as `Result Error ( state, bytes )`. **Each state value is
single-use**: reusing an older one returns an `Err`, because it would reuse a
nonce. Tested against the cacophony IK vectors. See `sky doc Std.Crypto.Noise`.

`Noise.initiatorWith` / `Noise.responderWith` take a typed `Suite` (`Sha256` or
`Blake2s`) and speak `Noise_IK_25519_ChaChaPoly_BLAKE2s` with `Blake2s` (the
hash WireGuard-family peers use). `Noise.protocolName` gives the protocol name
of a suite. Both sides must use the same suite.

### `Std.Crypto.Cpace` — password-authenticated key exchange (awaiting external review)

CPace (draft-irtf-cfrg-cpace-21, CPACE-X25519-SHA512) derives a strong 64-byte
key from a short shared code — a pairing code on a screen — without exposing
the code to an offline dictionary attack. `Cpace.start` (initiator) →
`Cpace.respond` (responder, returns its key) → `Cpace.finish` (initiator). A
wrong code is not an error: the keys differ, so confirm the key before you trust
it. **This module implements an Internet-Draft and has not had an independent
security review**; it passes the draft's test vectors. See `sky doc
Std.Crypto.Cpace`.

### `Std.Qr` — QR codes

```elm
import Std.Qr as Qr

case Qr.encode Qr.Medium "https://example.org/pair?code=482916" of
    Ok code -> Qr.view 4 code            -- Std.Ui element (inline SVG)
    Err e   -> Ui.text (errorToString e)
```

`Qr.encode : Qr.ErrorCorrection -> String -> Result Error Qr.QrCode` (levels
`Low`, `Medium`, `Quartile`, `High`; Err when the text is too long — 2953 bytes
at `Low`, 1273 at `High`), `Qr.size`, `Qr.isDark column row`, `Qr.rows`, and
the renderers `Qr.view` (`Std.Ui` element), `Qr.toSvg` (SVG document string)
and `Qr.toTerminal` (half-block characters in explicit black on white). Pure,
no cgo; it also runs in the Sky.Spa wasm client.

### `Bytes` — byte-buffer helpers (Sky.Core.Bytes)

`type alias Bytes = String` — Go strings ARE byte sequences;
`Bytes` is a typed alias for documenting "this string holds raw
bytes, not text".  Same value at runtime as the underlying
`String` so passing back and forth costs nothing.

| Function | Type | Notes |
|---|---|---|
| `Bytes.empty` | `Bytes` | `""` |
| `Bytes.length` | `Bytes -> Int` | Byte count (NOT rune count) |
| `Bytes.isEmpty` | `Bytes -> Bool` |  |
| `Bytes.fromString` | `String -> Bytes` | No-op (identity) — clarifies intent |
| `Bytes.toString` | `Bytes -> Maybe String` | `Nothing` on invalid UTF-8 |
| `Bytes.fromHex` | `String -> Maybe Bytes` | Case-insensitive |
| `Bytes.toHex` | `Bytes -> String` | Lowercase |
| `Bytes.fromBase64` | `String -> Maybe Bytes` | Standard base64 |
| `Bytes.toBase64` | `Bytes -> String` |  |
| `Bytes.append` | `Bytes -> Bytes -> Bytes` |  |
| `Bytes.slice` | `Int -> Int -> Bytes -> Bytes` | Byte indices |

### `Jwt` — JSON Web Tokens

```elm
import Sky.Core.Jwt as Jwt

token =
    Jwt.encode (Jwt.hs256 secret)
        (Jwt.claims
            |> Jwt.issuer "my-app"
            |> Jwt.subject "user-1"
            |> Jwt.expiresAt 1999999999
        )
-- token : Result Error String

payload = Jwt.decode (Jwt.hs256 secret) now token
-- → Result Error String (the verified payload JSON)
```

`encode` / `decode` support `HS256` (HMAC) and `RS256` (RSA — what
GitHub Apps and service accounts sign with). `decode` verifies the
signature *and* the `exp` / `nbf` claims against the `now` you pass
(unix seconds), then returns the payload JSON — decode it further
with `Sky.Core.Json.Decode`.

| Function | Type | Notes |
|---|---|---|
| `Jwt.hs256` | `String -> Algorithm` | HMAC-SHA256; the shared secret |
| `Jwt.rs256` | `String -> Algorithm` | RSA; PEM private key to `encode`, public key to `decode` |
| `Jwt.claims` | `Claims` | An empty claim set |
| `Jwt.issuer` / `subject` / `audience` / `jwtId` | `String -> Claims -> Claims` | Registered string claims (`iss`/`sub`/`aud`/`jti`) |
| `Jwt.expiresAt` / `notBefore` / `issuedAt` | `Int -> Claims -> Claims` | Registered time claims (`exp`/`nbf`/`iat`), unix seconds |
| `Jwt.withClaim` | `String -> JsonEnc.Value -> Claims -> Claims` | Any custom claim |
| `Jwt.encode` | `Algorithm -> Claims -> Result Error String` | Sign a token |
| `Jwt.decode` | `Algorithm -> Int -> String -> Result Error String` | Verify signature + `exp`/`nbf`; → payload JSON |

### `Encoding` — base64, base32, URL, hex

```elm
import Sky.Core.Encoding as Encoding

encoded = Encoding.base64Encode "hello"            -- "aGVsbG8="
decoded = Encoding.base64Decode encoded            -- Result Error String
urlSafe = Encoding.urlEncode "https://example.com/?q=hello world"
```

`base64Encode`, `base64Decode`, `base32Encode`, `base32Decode`, `base32EncodeNoPad`, `base32DecodeNoPad`, `base32HexEncode`, `base32HexDecode`, `urlEncode`, `urlDecode`, `hexEncode`, `hexDecode`. Encode functions return bare strings; decode functions return `Result Error String`.

The base32 functions follow RFC 4648: `base32*` is the standard alphabet
(`A`–`Z`, `2`–`7`), `base32Hex*` the extended-hex alphabet (`0`–`9`, `A`–`V`),
and the `NoPad` pair drops the `=` padding (the TOTP-secret form). The base32
decoders accept only the canonical text of some bytes: lower case, a line
break, the wrong padding or non-zero unused bits in the last symbol are an
`Err`, so one byte string has exactly one accepted text.

### `Json.Encode` / `Json.Decode` — JSON

```elm
import Sky.Core.Json.Encode as Enc
import Sky.Core.Json.Decode as Dec

-- Encode
payload =
    Enc.encode 0
        (Enc.object
            [ ( "name", Enc.string "Alice" )
            , ( "age", Enc.int 30 )
            ]
        )

-- Decode
case Dec.decodeString (Dec.field "name" Dec.string) payload of
    Ok name -> name
    Err _   -> "anonymous"
```

| Encoder | Type |
|---|---|
| `Enc.string` | `String -> Value` |
| `Enc.int` | `Int -> Value` |
| `Enc.float` | `Float -> Value` |
| `Enc.bool` | `Bool -> Value` |
| `Enc.null` | `Value` |
| `Enc.list` | `(a -> Value) -> List a -> Value` |
| `Enc.object` | `List (String, Value) -> Value` |
| `Enc.encode` | `Int -> Value -> String` (indent param) |
| `Enc.raw` | `String -> Result Error Value` — embed JSON text that is already serialised; invalid text is `Err InvalidInput` |

| Decoder | Type |
|---|---|
| `Dec.decodeString` | `Decoder a -> String -> Result Error a` — the string must be ONE JSON document; text after it is an `Err` |
| `Dec.decodeValue` | `Decoder a -> Value -> Result Error a` — run a decoder on a `Value` in memory; the same answer as `decodeString d (Enc.encode 0 v)` |
| `Dec.string`, `Dec.int`, `Dec.float`, `Dec.bool` | primitive decoders |
| `Dec.value` | `Decoder Value` — the JSON at this point, unchanged, as the `Value` type `Enc` uses (numbers keep their exact text) |
| `Dec.field` | `String -> Decoder a -> Decoder a` |
| `Dec.index` | `Int -> Decoder a -> Decoder a` |
| `Dec.list` | `Decoder a -> Decoder (List a)` |
| `Dec.map`, `Dec.map2`...`Dec.map5` | combine |
| `Dec.andThen` | dependent decoders |
| `Dec.succeed` / `Dec.fail` | constant decoders |
| `Dec.oneOf` | try decoders in order |
| `Dec.at` | `List String -> Decoder a -> Decoder a` (path traversal) |

`Value` is one type shared by both modules: a `Dec.value` result nests in `Enc.object` / `Enc.list` and writes back with `Enc.encode`. `Enc.encode` has no error result, so the one `Value` JSON cannot hold — a NaN or infinite `Float` (`Math.nan`, `Math.inf`, a Float overflow) — raises the classified `JsonEncodeFailure` panic (a 500 for that request in a server, exit 1 in a CLI). It never returns an empty string. Check a Float you do not control with `Math.isNaN` first.

**Key order.** `Enc.object` writes its keys in the order given, and `Enc.raw`
writes its text as it came. A `Dec.value` result does NOT keep the key order of
its source: its objects are written back with their keys sorted byte-wise at
every depth (`{"b":1,"a":2}` becomes `{"a":2,"b":1}`), and whitespace is not
kept. Keep text whose exact bytes matter (a signed payload) with `Enc.raw`.

For long records use the pipeline form:

```elm
import Sky.Core.Json.Decode.Pipeline as Pipeline

userDecoder =
    Dec.succeed User
        |> Pipeline.required "id"   Dec.int
        |> Pipeline.required "name" Dec.string
        |> Pipeline.optional "age"  Dec.int 0
```

### `Uuid` — UUID generation + parsing

```elm
import Sky.Core.Uuid as Uuid

myId : String
myId = Uuid.v4   -- "f47ac10b-58cc-4372-a567-0e02b2c3d479"
```

`v4` (random), `v7` (time-ordered), `parse` (validate string).

### `Std.Decimal` — arbitrary-precision decimal arithmetic

```elm
import Std.Decimal as Dec
```

For money, billing, tax, invoices — anything where exact fractional value matters. `Decimal` is opaque; backed by `shopspring/decimal` at the runtime, so `0.1 + 0.2 == 0.3` exactly.

| Surface | Signature |
|---|---|
| `fromString` / `fromInt` / `fromFloat` | `... -> Result Error Decimal` / `Int -> Decimal` / `Float -> Decimal` |
| `fromMinor places minor` | `Int -> Int -> Decimal` (cents → dollars: `fromMinor 2 12345` → `123.45`) |
| `zero` / `one` / `oneHundred` | `Decimal` constants |
| `add` / `sub` / `mul` | `Decimal -> Decimal -> Decimal` |
| `div` / `mod` | `Decimal -> Decimal -> Result Error Decimal` (Err on /0) |
| `neg` / `abs` | `Decimal -> Decimal` |
| `round n` / `roundHalfUp n` / `truncate n` | `Int -> Decimal -> Decimal` (`round` is banker's) |
| `floor` / `ceil` | `Decimal -> Decimal` |
| `eq` / `neq` / `lt` / `lte` / `gt` / `gte` / `compare` | `Decimal -> Decimal -> Bool` (or `Int` for compare) |
| `min` / `max` | `Decimal -> Decimal -> Decimal` |
| `isZero` / `isPositive` / `isNegative` | `Decimal -> Bool` |
| `percentOf` / `addPercent` / `subPercent` | `Decimal -> Decimal -> Decimal` (pct as Decimal: `Dec.fromInt 10` = 10%) |
| `toString` / `toStringFixed n` / `toFloat` / `toInt` / `toMinor n` | `Decimal -> String` (etc.) |
| `formatWith` | `{thousands, decimal, places} -> Decimal -> String` (US/EU/FR conventions) |
| `sum` | `List Decimal -> Decimal` |

### `Std.Money` — currency-aware Money built on Decimal

```elm
import Std.Money as Money exposing (Money, Currency)
```

ISO 4217 minor-unit awareness (JPY=0dp, USD=2dp, BHD=3dp). `Currency` is a typed enum of 50+ codes (USD, EUR, GBP, JPY, CHF, AUD, CAD, …, BTC, ETH, USDT, USDC) plus `CurrencyRaw String` for the long tail. All arithmetic enforces currency match.

| Surface | Signature |
|---|---|
| `fromMajor c n` / `fromMinor c n` / `fromString c s` | `Currency -> ... -> Money` (string is `Result Error Money`) |
| `zero c` / `zeroOf c` | `Currency -> Money` |
| `amount` / `currency` / `currencyCode` | `Money -> Decimal` / `Currency` / `String` |
| `add` / `sub` | `Money -> Money -> Money` (currency-matched; no-op on mismatch) |
| `mul scalar m` | `Decimal -> Money -> Money` |
| `neg` / `abs` | `Money -> Money` |
| `allocate parts m` | `Int -> Money -> List Money` (fair split — `$100/3 → [$33.34, $33.33, $33.33]`, sum-preserving) |
| `sumOf c xs` | `Currency -> List Money -> Money` |
| `eq` / `neq` / `lt` / `lte` / `gt` / `gte` / `compare` | `Money -> Money -> Bool` (Int for compare) |
| `isZero` / `isPositive` / `isNegative` | `Money -> Bool` |
| `percentOf` / `addPercent` / `subPercent` | `Decimal -> Money -> Money` |
| `format` / `formatWithCode` | `Money -> String` (`"$108.88"` / `"USD 108.88"`) |
| `toMinor` | `Money -> Int` |
| `minorUnits` / `symbol` / `currencyName` | `Currency -> ...` |
| `knownCurrency` | `Currency -> Bool` (False only for `CurrencyRaw _`) |
| `isKnownCode` | `String -> Bool` (raw ISO code predicate — use for form input) |
| `parseCurrency` | `String -> Currency` (falls back to `CurrencyRaw` on unknown) |
| `setRate from to rate` / `getRate` / `hasRate` / `clearRates` | FX rate registry (process-local) |
| `convert to m` | `Currency -> Money -> Result Error Money` |

### `Std.Time` — IANA-zone helpers complementing kernel `Time`

```elm
import Std.Time as Stime
```

Embedded `time/tzdata`, so it works in containers without
`/usr/share/zoneinfo`. **Note** the import alias `Stime` rather
than `Time` — the kernel `Time` already owns that name. Zones are
IANA strings (`"UTC"`, `"America/New_York"`, `"Asia/Tokyo"`); a bad
zone name returns `Err Error`. Timestamps are unix-millis `Int`,
matching `Time.unixMillis`. 32 entries.

| Surface | Signature / behaviour |
|---|---|
| `inZone zone ms` | `Result Error String` — RFC 3339 in the given zone |
| `formatInZone zone layout ms` | `Result Error String` — custom Go layout |
| `addMonths n ms` / `addYears n ms` | `Int -> Int -> Int` — **clamped** (Jan 31 + 1 month → Feb 28/29, NOT Mar 3) |
| `addDays` / `addHours` / `addMinutes` / `addSeconds` | `Int -> Int -> Int` — non-clamped arithmetic |
| `startOfDay zone` / `startOfWeek` / `startOfMonth` / `startOfYear` | `String -> Int -> Result Error Int` — floor helpers (week starts Monday, ISO) |
| `endOfDay` / `endOfMonth` / `endOfYear` | `String -> Int -> Result Error Int` — ceiling helpers |
| `year` / `month` / `day` / `dayOfWeek` | `String -> Int -> Result Error Int` — components (`dayOfWeek` is ISO Mon=1..Sun=7) |
| `dayOfYear` / `weekOfYear` | `String -> Int -> Result Error Int` — ISO 8601 week |
| `isWeekend` | `String -> Int -> Result Error Bool` |
| `isLeapYear y` | `Int -> Bool` |
| `daysInMonth y m` | `Int -> Int -> Int` (handles leap Feb) |
| `diffDays` / `diffHours` / `diffMinutes` / `diffSeconds` | `Int -> Int -> Int` (millis → unit) |
| `fromParts zone y m d h mi s` | `... -> Result Error Int` — construct from components |
| `zoneOffset zone ms` / `zoneName zone ms` | zone metadata at that instant |
| `utc` | `String` — the `"UTC"` constant |

---

## Effects (`Task Error a`)

These touch the outside world. They compose uniformly — `Task.parallel`, `Cmd.perform`, `Task.andThen`.

### `Task` — the effect monad

```elm
import Sky.Core.Task as Task

main =
    Task.succeed 42
        |> Task.andThen (\n -> println (String.fromInt n))
        |> Task.run
```

| Function | Type | Notes |
|---|---|---|
| `Task.succeed` | `a -> Task e a` | Lift a pure value |
| `Task.fail` | `e -> Task e a` | Construct a failed task |
| `Task.map` | `(a -> b) -> Task e a -> Task e b` | Transform success |
| `Task.andThen` | `(a -> Task e b) -> Task e a -> Task e b` | Sequence effects |
| `Task.mapError` | `(e -> e2) -> Task e a -> Task e2 a` | Transform failure |
| `Task.onError` | `(e -> Task e2 a) -> Task e a -> Task e2 a` | Recover from failure |
| `Task.sequence` | `List (Task e a) -> Task e (List a)` | Run sequentially |
| `Task.parallel` | `List (Task e a) -> Task e (List a)` | Run concurrently (goroutines); first error short-circuits |
| `Task.lazy` | `(() -> a) -> Task e a` | Defer computation |
| `Task.spawn` | `Task e a -> Task e ()` | Start a task on a background goroutine and return at once. Its result is discarded; a panic in it is recovered and written to the log as a classified panic line |
| `Task.Step state a` | ADT | `Loop state \| Done a` — what a `Task.loop` step returns: go again with a new state, or stop with a result |
| `Task.loop` | `(state -> Task e (Step state a)) -> state -> Task e a` | Stack-safe loop: run the step, then again on each `Loop` state, until `Done`. An `Err` stops it. The clearest form for a loop with explicit state (`andThen` recursion is stack-safe too) |
| `Task.forever` | `Task e a -> Task e b` | Re-run a task until it fails, then fail with that error. Stack-safe. The free `b` says it never succeeds |
| `Task.run` | `Task e a -> Result e a` | Force at the boundary |
| `Task.fromResult` | `Result e a -> Task e a` | Bridge from Result |
| `Task.andThenResult` | `(a -> Result e b) -> Task e a -> Task e b` | Chain Result step after Task |
| `Task.RetryPolicy e` | record alias | `{ maxAttempts : Int, baseMs : Int, jitter : Bool, kind : Int, shouldRetry : ShouldRetry e }` — `e` flows from the body Task; build via `linearBackoff` / `exponentialBackoff` / `defaultRetryPolicy` then decorate |
| `Task.ShouldRetry e` | ADT | `RetryAlways \| RetryWhen (e -> Bool)` — HM-pure predicate (replaces `shouldRetry : any` from v0.15.44); portable to statically-typed backends (Rust / WASM) without runtime boxing |
| `Task.retryAlways` | `ShouldRetry e` | Pure-Sky `RetryAlways` sentinel — the default in every fresh policy |
| `Task.linearBackoff` | `Int -> Int -> RetryPolicy e` | (maxAttempts, delayMs) — same delay every retry |
| `Task.exponentialBackoff` | `Int -> Int -> RetryPolicy e` | (maxAttempts, baseMs) — `baseMs * 2^(n-1)`, capped at 30 s |
| `Task.defaultRetryPolicy` | `RetryPolicy e` | Sensible default: 3 attempts, 500 ms exponential base, no jitter, `RetryAlways` — start here when building with `with*` helpers |
| `Task.withMaxAttempts` | `Int -> RetryPolicy e -> RetryPolicy e` | Builder helper — override `maxAttempts` |
| `Task.withBaseMs` | `Int -> RetryPolicy e -> RetryPolicy e` | Builder helper — override `baseMs` |
| `Task.withKind` | `Int -> RetryPolicy e -> RetryPolicy e` | Builder helper — override backoff `kind` (`0 = linear, 1 = exponential`) |
| `Task.withJitter` | `RetryPolicy e -> RetryPolicy e` | Randomise delay in `[0.5×, 1.5×]` to spread retry waves |
| `Task.withRetryOn` | `(e -> Bool) -> RetryPolicy e -> RetryPolicy e` | Builder alias for `retryOn` — wraps predicate in `RetryWhen` |
| `Task.retryOn` | `(e -> Bool) -> RetryPolicy e -> RetryPolicy e` | Predicate-gate retry (e.g. transient-vs-validation) — sets `shouldRetry = RetryWhen predicate` |
| `Task.retryWith` | `RetryPolicy e -> Task e a -> Task e a` | Drive task up to maxAttempts; first Ok wins, last Err otherwise |
| `Task.map2` … `Task.map5` | `(a -> b -> c) -> Task e a -> Task e b -> Task e c` (…up to 5) | Combine N tasks with an N-ary function. Forces left to right, each task exactly once; a failure short-circuits the tasks to its right |
| `Task.andMap` | `Task e a -> Task e (a -> b) -> Task e b` | Applicative application — VALUE first, FUNCTION second, matching `Maybe.andMap` / `Result.andMap`. Forces the function task first |


**Loops: `Task.loop`, or plain `andThen` recursion.** A Task is data run by one interpreter (the Task trampoline, v0.27.0). It pops an `andThen` before it runs the task the continuation returns, so recursion through `andThen` or `onError` runs at a constant Go stack: two million iterations of `step n = work |> Task.andThen (\_ -> step (n + 1))` finish. `Task.sequence` over a long list is folded by the same interpreter. `Task.loop` is the clearer form when the loop has explicit state, and `Task.forever` for a loop that only stops on an error. A recursive call that is not behind a continuation (`step (n - 1) |> Task.map f`) is evaluated while the task is built, and grows the stack like any strict recursion (`docs/KNOWN_LIMITATIONS.md`):

```elm
import Sky.Core.Task as Task exposing (Step(..))

countTo : Int -> Task Error Int
countTo limit =
    Task.loop (countStep limit) 0

countStep : Int -> Int -> Task Error (Step Int Int)
countStep limit n =
    if n >= limit then
        Task.succeed (Done n)

    else
        Task.succeed (Loop (n + 1))
```

`Loop` and `Done` come from `Sky.Core.Task`: import them with `exposing (Step(..))`, or write `Task.Loop` / `Task.Done` after `import Sky.Core.Task as Task`.

### `Cmd` / `Sub` — Sky.Live commands and subscriptions

```elm
import Std.Cmd as Cmd
import Std.Sub as Sub

update msg model =
    case msg of
        LoadData ->
            ( { model | loading = True }
            , Cmd.perform (Http.get "/api/data") DataLoaded
            )
```

| Function | Type | Notes |
|---|---|---|
| `Cmd.none` | `Cmd msg` | No-op |
| `Cmd.perform` | `Task err a -> (Result err a -> msg) -> Cmd msg` | Run task, dispatch result as Msg |
| `Cmd.batch` | `List (Cmd msg) -> Cmd msg` | Concurrent batch |
| `Cmd.publish` | `String -> any -> Cmd msg` | Broadcast payload to every Sky.Live session subscribed to topic — see [skylive/pubsub.md](skylive/pubsub.md) |
| `Cmd.toIsland` | `String -> String -> Json.Encode.Value -> Cmd msg` | Send a command (island id, name, JSON payload) to a widget island's `command` handler, after the update — see [Widget islands](skyui/overview.md#widget-islands--third-party-js-widgets) |
| `Sub.none` | `Sub msg` | No subscription |
| `Sub.every` | `Int -> msg -> Sub msg` | Dispatch `msg` every N ms |
| `Sub.subscribeTopic` | `String -> (any -> msg) -> Sub msg` | Receive pub/sub broadcasts on topic; decoder turns payload into a Msg |
| `Sub.batch` | `List (Sub msg) -> Sub msg` | Combine timer + topic + others |

### `Time` — clock + duration

```elm
import Sky.Core.Time as Time

now =
    Time.now
        |> Task.andThen (\t -> println (Time.formatISO8601 t))
```

`now`, `unixMillis`, `sleep`, `every`, `format`, `formatISO8601`, `formatRFC3339`, `formatHTTP`, `addMillis`, `diffMillis`, `timeString`.

> For zone-aware formatting, parsing, calendar arithmetic, and
> period boundaries reach for [`Std.Time`](#stdtime--iana-zone-helpers-complementing-kernel-time).

### `Random` — pseudo-random generation

```elm
import Sky.Core.Random as Random

dice = Random.int 1 6   -- Task Error Int
```

`int : Int -> Int -> Task Error Int` (inclusive low/high) and
`float : Float -> Float -> Task Error Float`. For cryptographic
entropy use [`Crypto.randomBytes` / `Crypto.randomToken`](#crypto--hashes-mac-signatures-entropy).

### `Http` — HTTP client

```elm
import Sky.Core.Http as Http

response =
    Http.get "https://api.example.com/users"
        |> Task.map (\resp -> resp.body)
        |> Task.andThen println

-- HttpResponse is a typed record (v0.15.44+) — annotate and
-- destructure directly:
--   HttpResponse = { status : Int, body : String, headers : Dict String String }
```

`HttpResponse` is a typed record alias.  Annotate handlers as
`resp : HttpResponse` and read `.status` / `.body` / `.headers`
directly — no opaque kernel boundary any more.

The builder API on `HttpRequest` covers custom headers, timeout,
redirect policy:

```elm
req =
    Http.defaultRequest "https://api.example.com/v1/foo"
        |> Http.withMethod "POST"
        |> Http.withHeader "Authorization" ("Bearer " ++ token)
        |> Http.withBody jsonBody
        |> Http.withTimeout 60000              -- 60 s; 0 disables
```

Pair with `Task.retryWith` for flaky upstreams:

```elm
fetchData =
    Task.retryWith
        (Task.exponentialBackoff 5 500 |> Task.withJitter)
        (Http.request req)
```

`get`, `post`, `request` (custom method/headers/timeout).
`parseQuery` parses a URL query string into a `Dict String String`
(pure — backed by Go's `net/url`, proper percent-decoding).

For **streaming response bodies** (LLM completions, SSE, large
downloads), use `Sky.Core.Http.Stream`:

```elm
import Sky.Core.Http.Stream as HttpStream exposing (StreamId, ChunkEvent(..))

-- Cmd.perform kicks off the request; chunks arrive via Sub.
( model, Cmd.perform (HttpStream.open req) StreamOpened )

-- subscriptions: attach `chunks` only while a stream is live.
subscriptions model =
    case model.activeStream of
        Just sid -> HttpStream.chunks sid Chunked
        Nothing  -> Sub.none
```

See [`docs/skylive/http-streaming.md`](skylive/http-streaming.md)
for the full design + `examples/28-streaming-chat` for the
canonical pattern.

### `WebSocket` — bidirectional sockets (v0.15.46)

For bidirectional, long-lived connections (collab editor ops,
multiplayer game state, bidirectional LLM chat, financial feeds),
use `Sky.Core.WebSocket` (client) + `Sky.Http.Server.WebSocket`
(server-side upgrade).

**Client side (Sky.Core.WebSocket):**

```elm
import Sky.Core.WebSocket as Ws exposing (WebSocketMessage(..))
import Sky.Core.Cmd as Cmd
import Sky.Core.Sub as Sub

-- Open the connection via Cmd.perform; the runtime returns a
-- typed `WebSocket` handle.
( model, Cmd.perform (Ws.connect "wss://api.example.com/feed") Connected )

-- Subscribe to incoming frames while the socket is live.
subscriptions model =
    case model.socket of
        Just sock ->
            Sub.batch
                [ Ws.onMessage sock GotFrame
                , Ws.onClose   sock SocketClosed
                ]

        Nothing ->
            Sub.none

-- Send a text frame.  Blocks up to 30 s if the write buffer is full.
update msg model =
    case msg of
        SendPing sock ->
            ( model, Cmd.perform (Ws.send sock "ping") Sent )

        GotFrame (Text text) ->
            -- handle incoming text frame
            ( { model | latest = text }, Cmd.none )

        GotFrame (Binary bytes) ->
            ( { model | latestBlob = bytes }, Cmd.none )
```

**Client side from a Task program (v0.27).** A CLI, a worker or a bridge has
no update loop to deliver a Sub to. It reads the socket with Tasks instead:

```elm
-- receive : WebSocket -> Task Error (Maybe WebSocketMessage)
-- receiveWithin : Int -> WebSocket -> Task Error (Maybe WebSocketMessage)
-- forEachMessage : WebSocket -> (WebSocketMessage -> Task Error ()) -> Task Error ()
Ws.connect "wss://api.example.com/feed"
    |> Task.andThen
        (\sock ->
            Ws.forEachMessage sock
                (\frame ->
                    case frame of
                        Text s -> Log.println s
                        Binary _ -> Task.succeed ()
                )
        )
```

`receive` returns `Just frame` per frame, `Nothing` once the socket has
closed cleanly (and on every later call), and `Err` (`Network`) when the read
fails. `receiveWithin ms` adds `Err` (`Timeout`); a timeout consumes no frame
and leaves the socket open. `forEachMessage` runs a body per frame until the
socket closes, stops at the first body `Err`, and always closes the socket.
Several Tasks may receive from one socket (each frame goes to one of them).

A socket has **one reader**: the first consumer claims it. `receive` on a
socket a Sub reads returns `Err` (`InvalidInput`); a Sub on a socket a Task
reads is ignored and logged. A Task reader never loses the socket to a slow
consumer: the runtime stops reading from the network until the Task receives
again (TCP slows the peer), and the heartbeat and the idle reaper treat the
waiting socket as alive. A socket whose heartbeat pings are answered is never
closed as idle.

**Server side (Sky.Http.Server.WebSocket):** turn any
`Sky.Http.Server` route into a WebSocket upgrade endpoint.

```elm
import Sky.Http.Server as Server
import Sky.Http.Server.WebSocket as Ws

handleWs : Request -> Task Error Response
handleWs req =
    Ws.upgrade req
        (Ws.defaultCfg
            |> Ws.withOnConnect (\sock ->
                Ws.sendToClient sock "welcome!")
            |> Ws.withOnMessage (\sock msg ->
                Ws.sendToClient sock ("echo: " ++ msg))
            |> Ws.withOriginPatterns
                [ "https://*.example.com" ]
        )

main =
    Server.listen 8000
        [ Server.get "/ws" handleWs
        ]
```

`withOnMessage` gives the handler a `String` for text and binary frames
alike. To know the frame type, use `withOnFrame` (v0.27), which hands the
handler the client module's `WebSocketMessage`:

```elm
import Sky.Core.WebSocket exposing (WebSocketMessage(..))

onFrame sock frame =
    case frame of
        Text s -> Ws.sendToClient sock ("echo: " ++ s)
        Binary bytes -> Ws.sendBinaryToClient sock bytes

-- Ws.defaultCfg |> Ws.withOnFrame onFrame
```

Once `withOnFrame` is set, `onMessage` is not called.

| Concern | Default |
|---|---|
| Handshake timeout | 30 s |
| Heartbeat ping | 30 s (set `pingInterval = 0` to disable) |
| Max message size | 1 MiB (`withMaxMessageBytes`) |
| Origin gate | no `Origin` (a native client) and a same-host page always pass. With no `withOriginPatterns`: production refuses every upgrade (403); outside production, loopback pages on any port (`localhost:5173`) and hosts in `SKY_ALLOWED_HOSTS` pass, every other origin gets 403 |
| Read buffer | 64 frames per socket (bounded). A Sub that stops draining for 30 s loses the socket; a Task reader (`receive`) applies backpressure instead |
| `send` backpressure | blocks up to 30 s on a slow consumer |

`broadcast` fans a single text frame across a list of peers and
tolerates partial failure (one slow / dead peer doesn't poison
the others — those connections are closed silently).

**Stdlib-typed-record convention (v0.15.46+).** Every public
typed record (`WebSocketCfg`, `WebSocketServerCfg`,
`HttpRequest`, …) ships with a `default*` constructor and one
`with*` builder per field.  Always build via the builders
(`Ws.defaultCfg "wss://x" |> Ws.withTimeout 5000`) rather than
record literals — adding a new optional field in a future patch
release won't break your call sites.

See `examples/33-websocket-echo` for the canonical pattern.

### `File` — filesystem

```elm
import Sky.Core.File as File

readme =
    File.readFile "README.md"
        |> Task.andThen (\content -> println content)
```

`readFile`, `readFileLimit`, `readFileBytes`, `writeFile`, `append`, `mkdirAll`, `readDir`, `exists`, `remove`, `isDir`, `tempFile`, `tempDir`, `copy`, `rename`.

`exists` / `isDir` return `Task Error Bool` (effects — the disk could be unmounted between successive calls).  `tempFile` / `tempDir` create uniquely-named entries in the system temp dir and return the absolute path; caller is responsible for `remove`-ing when done.

### `Io` — stdin / stdout / stderr

`readLine`, `writeStdout`, `writeStderr` — all `Task Error
…`-typed. For password input with stdin echo disabled, use
[`Sky.Cli.readPassword`](skytui/overview.md#skycli-password-mode).

### `System` — environment + arguments

```elm
import System

apiKey =
    System.getenvOr "API_KEY" ""    -- bare String (default supplied)

main =
    System.args
        |> Task.andThen (\args -> println ("Got " ++ String.fromInt (List.length args) ++ " args"))
```

| Function | Type | Notes |
|---|---|---|
| `System.args` | `Task Error (List String)` | All command-line args |
| `System.getArg` | `Int -> Task Error (Maybe String)` | Single positional arg |
| `System.getenv` | `String -> Task Error String` | Required env var (errors if missing) |
| `System.getenvOr` | `String -> String -> String` | **Bare** — default supplied |
| `System.getenvInt` | `String -> Task Error Int` | Parsed int env var |
| `System.getenvBool` | `String -> Task Error Bool` | Parsed bool env var (`true`/`false`/`1`/`0`) |
| `System.cwd` | `Task Error String` | Current working directory |
| `System.exit` | `Int -> a` | **Diverging** — process termination |
| `System.loadEnv` | `Task Error ()` | Load `.env` file |
| `System.setenv` | `String -> String -> Task Error ()` | Set a process env var (v0.11.5+) |
| `System.unsetenv` | `String -> Task Error ()` | Remove a process env var (v0.11.5+, idempotent) |

> `System.exit` has a polymorphic return so it works in any case branch — no need to make every other branch Task-shaped.

**Env-var namespace prefix (v0.11.5+).** Sky's internal runtime reads (Sky.Live, Std.Auth, Std.Log, Std.Db) use the `SKY_` prefix by default — `SKY_LIVE_PORT`, `SKY_AUTH_TOKEN_TTL`, etc. Set `[env] prefix = "FENCE"` in `sky.toml` to switch the binary's namespace to `FENCE_LIVE_PORT`, `FENCE_AUTH_TOKEN_TTL`, etc. Useful when running multiple Sky binaries on the same host. User-supplied env-var names (passed to `System.getenv`) are unaffected — only Sky's internal reads route through the prefix.

### `Process` — subprocess execution

```elm
import Sky.Core.Process as Process

result =
    Process.run "ls" [ "-la" ]
        |> Task.andThen (\output -> println output)
```

`Process.run` runs a program to completion and returns its stdout. (`exit`,
`getEnv`, `getCwd`, `loadEnv` moved to `System` in v0.10.0.)

#### Streaming child processes (`Process.spawn`, v0.27.0)

`spawn` starts a long-running child and returns an opaque `Process`. Build the
command with `command` and the `with*` helpers:

| Builder | Effect |
|---|---|
| `command program` | a program path, or a name looked up on `PATH` |
| `withArgs args` | the arguments |
| `withEnv pairs` / `withClearEnv` | add variables / start from an empty environment |
| `withCwd dir` | the working directory |
| `withPty { cols, rows }` | run on a pseudo-terminal (Linux and macOS) |
| `withBufferSize bytes` | the output ring size per stream (default 1 MiB) |

Output goes into a bounded ring per stream. Every byte has an absolute offset,
so a reader keeps its own position and can stop and resume. The child never
waits for a slow reader: when the ring is full the oldest bytes are
overwritten, and a reader that asks for them gets `dropped = True`.

```elm
-- Read stdout to the end: pass each chunk's `next` to the following call.
readAll : Process -> Int -> String -> Task Error String
readAll p offset acc =
    Process.readFrom p Stdout offset
        |> Task.andThen
            (\chunk ->
                if chunk.eof then
                    Task.succeed (acc ++ chunk.data)

                else
                    readAll p chunk.next (acc ++ chunk.data)
            )
```

| Function | Type |
|---|---|
| `spawn` | `Command -> Task Error Process` |
| `readFrom` | `Process -> Stream -> Int -> Task Error Chunk` (waits for data or end of file) |
| `readWithin` | `Int -> Process -> Stream -> Int -> Task Error Chunk` (returns an empty chunk after the timeout) |
| `events` | `Process -> (Event -> msg) -> Sub msg` (`Output Stream Chunk`, then one `Exited ExitStatus`) |
| `write` / `closeStdin` | `Process -> String -> Task Error ()` / `Process -> Task Error ()` |
| `resize` | `Process -> { cols : Int, rows : Int } -> Task Error ()` (PTY only; `Err InvalidInput` otherwise) |
| `screen` | `{ view : String, gen : Int, full : Bool, waitMs : Int } -> Process -> Task Error Screen` (the next frame of the terminal screen the runtime emulates, for the terminal widget `view`; what `Std.Ui.Terminal` calls) |
| `kill` | `Process -> Signal -> Task Error ()` (`Interrupt`, `Terminate`, `Kill`, `Hangup`; to the whole process group) |
| `wait` | `Process -> Task Error ExitStatus` (`ExitCode Int` or `Signalled Int`) |
| `pid` / `close` | the OS process id / kill if running, release, forget |

`Chunk` is `{ data, from, next, dropped, eof }`. With a PTY all output is one
merged stream on `Stdout`. To show a PTY child in a Sky.Live page, use
`Std.Ui.Terminal`: it reads the process's screen with `screen`. The runtime
emulates the terminal on the server (every output byte runs through a VT100 /
xterm screen as it is written) and sends the widget screen-diff frames, so a
remounted terminal is repainted from the screen and its last 1000 scrollback
lines (see [Terminal](skyui/overview.md#terminal--a-pty-in-the-page-stduiterminal)).
`Screen` is `{ changed, frame, eof }`: `frame` is the widget command payload,
`eof` says the process ended and its exit line is on the screen.

**One consumer mode per process.** Read the output from a Task (`readFrom`,
`readWithin`) or from a Sub (`events`), not both. The first one used fixes the
mode: `readFrom` on a process an `events` Sub reads is `Err InvalidInput`, and
an `events` Sub on a process a Task reads is ignored and logged. `events`
works in Sky.Live, Sky.Cli, Sky.Tui and Sky.Webview apps; dropping the Sub stops
delivery, and adding it again continues where it stopped.

**Lifecycle.** The child runs in its own process group; `kill` reaches its
grandchildren. An exited child is always reaped (no zombies). A child spawned
from a Sky.Live session is closed when the session ends, and so when its app
stops (`App.stop`). Every child still running is killed when the program exits.
A PTY on an OS other than Linux and macOS is `Err Unavailable`.

### `Std.Watch` — file-system change notification (v0.27.0)

```elm
import Std.Watch as Watch exposing (Change(..))

Watch.watch [ "src" ] (Watch.defaultOptions |> Watch.withRecursive True |> Watch.withIgnore [ ".git", "*.swp" ])
    |> Task.andThen (\w -> Watch.next w)
```

| Function | Type |
|---|---|
| `watch` | `List String -> Options -> Task Error Watcher` |
| `next` | `Watcher -> Task Error (List Change)` (waits for the next batch) |
| `changes` | `Watcher -> (List Change -> msg) -> Sub msg` |
| `close` | `Watcher -> Task Error ()` |

`Change` is `Created`, `Modified`, `Removed` (a path), `Renamed` (old and new
path) or `Overflow`. Options: `withRecursive`, `withDebounce ms` (default 50),
`withIgnore patterns` (a pattern without `/` matches any path component; a
pattern with `/` matches the path relative to the watched directory).

Changes are coalesced: the runtime waits until the tree is quiet for the
debounce window and delivers one batch in which each path appears once (a file
created then written is `Created`; created then deleted is not reported;
deleted then re-created is `Modified`; a move inside the tree is `Renamed`).
When the OS queue overflowed, or batches piled up unread, the next batch is
exactly `[ Overflow ]`: rescan. One consumer mode per watcher, as for
`Process`. Linux uses inotify and macOS kqueue, both from the Go standard
library; other systems return `Err Unavailable`.

### `Db` / `Auth` / `Log`

These are big enough to deserve their own pages:

- **[Std.Db overview](skydb/overview.md)** — SQLite + Postgres, one API
- **[Std.Auth overview](skyauth/overview.md)** — bcrypt, JWT, register / login
- **[Std.Log](#stdlog)** — see below

### `Std.Codec` + `Std.Db.Store` — codec-driven persistence (v0.19)

The **recommended default** for record-shaped tables: write ONE `Codec` per type
(`Std.Codec`) and `Std.Db.Store` drives the schema, reads, and writes — no
hand-written SQL, no row mappers. The same codec also serves JSON.

- **`Std.Codec`** — `Codec.auto blank` reflection-derives from a zero-value witness
  (scalars → columns, `Maybe` → nullable, list/nested/ADT → JSON blob, nullary enum
  → readable name); **snake_case** columns/keys by default (`Codec.autoCamel` keeps
  camelCase). **`Codec.autoWith [ ("active", intBool) ] blank`** overrides specific
  fields' codecs while auto-deriving the rest (a Bool stored 0/1, a custom enum
  format) — no full hand-written codec for a one-field tweak. `toJson` / `fromJson`
  / `fromJsonSafe`; explicit `object`/`field`/`taggedUnion`/`enum` for full control.
  Pure Sky.
- **`Std.Db.Store`** — `fromCodec |> primaryKey`/`serial`/`unique`/`defaultNow`/
  `touchOnUpdate`/`defaultWith`/`generated` (schema), `insert`/`insertMany`/`update`/
  `updateWhere`/`upsert`/`delete`/`deleteWhere` (writes), **`setFields`/`updateFields`**
  (partial-column PATCH — `SET` only the named columns, by PK or `Cond`) + **`adjust`**
  (atomic `SET col = col + delta` for counters/stock/balances), `all`/`findBy` + the
  composable query builder (`where_`/`and_`/`or_`/order/limit/`toList`/`count`) +
  **`selectRaw codec sql params`** for typed JOIN/aggregate reads. Deliberately a
  single-table mapper, not an ORM.

Full walkthrough: **[Std.Db overview → Std.Db.Store](skydb/overview.md#stddbstore--stdcodec--codec-driven-persistence-recommended-default)**.
Exact signatures: `sky doc Std.Db.Store` · `sky doc Std.Codec`.

### `Std.Db.Decode` — typed DB row decoders (v0.15.45)

Mirror of `Sky.Core.Json.Decode`'s combinator shape but targets SQL
row maps instead of JSON values. Replaces the `Db.getString "field"
row` / `Db.getInt "field" row` boilerplate with declarative
decoders.

```elm
import Std.Db.Decode as DbDecode

type alias User =
    { id : Int, name : String, email : String, age : Maybe Int }

userDecoder : Decoder User
userDecoder =
    DbDecode.succeed (\i n e a -> { id = i, name = n, email = e, age = a })
        |> DbDecode.andMap (DbDecode.int "id")
        |> DbDecode.andMap (DbDecode.string "name")
        |> DbDecode.andMap (DbDecode.string "email")
        |> DbDecode.andMap (DbDecode.nullable (DbDecode.int "age"))

users : Db -> Task Error (List User)
users db = Db.queryDecode db "SELECT id, name, email, age FROM users" [] userDecoder

userById : Db -> Int -> Task Error (Maybe User)
userById db uid = Db.getByIdDecode db "users" uid userDecoder
```

Surface: `string` / `int` / `float` / `bool` / `money` / `nullable` (per-column primitives), `succeed` / `fail`, `map` / `andThen` / `andMap`, `map2` / `map3` / `map4` / `map5`, `required` / `optional` (pipeline-style). See [`docs/skydb/overview.md`](skydb/overview.md) for the full decoder pipeline pattern.

### `Std.Db.SqlValue` — typed SQL parameter binding (v0.16.26)

Mixed-type SQL params (`INSERT … VALUES (?, ?, ?)` with a `String + Maybe Int + Bool` tuple) flow through `Db.exec` / `Db.query` as a homogeneous `List SqlValue` with full per-column type fidelity to the driver. Closes the no-stringify gap — the recursive `SqlNull SqlValue` carries a type-witness so the driver knows what column type to bind NULL as.

```elm
import Std.Db as Db exposing (SqlValue(..), SqlField(..))
import Std.Money as Money

-- INSERT with mixed types
saveOrder : Db -> Int -> String -> Money -> Maybe Int -> Task Error Int
saveOrder conn orderId customer total maybePaidAt =
    Db.exec conn
        "INSERT INTO orders (id, customer, total, paid_at) VALUES (?, ?, ?, ?)"
        [ SqlInt orderId
        , SqlString customer
        , SqlMoney total
        , Db.fromMaybeTime maybePaidAt   -- nullable column
        ]

-- PATCH-style partial update — only SetField columns appear in the SQL
updateOrder : Db -> Int -> Maybe String -> Bool -> Task Error Int
updateOrder conn orderId maybeStatus refunded =
    Db.updateFields conn "orders"
        [ ("id", SqlInt orderId) ]                                       -- WHERE
        [ ( "status"
          , case maybeStatus of
                Just s  -> SetField (SqlString s)
                Nothing -> OmitField                                     -- leave alone
          )
        , ( "refunded", SetField (SqlBool refunded) )
        ]
```

Variants (9 total) — `SqlString` / `SqlInt` / `SqlFloat` / `SqlBool` / `SqlBytes` / `SqlDecimal` / `SqlTime` / `SqlMoney` / `SqlNull SqlValue`. Money serialises lossless as `"ISO_CODE AMOUNT"` TEXT; round-trip via `Db.Decode.money`. Maybe-lifting helpers: `fromMaybeString` / `fromMaybeInt` / `fromMaybeFloat` / `fromMaybeBool` / `fromMaybeBytes` / `fromMaybeDecimal` / `fromMaybeTime` / `fromMaybeMoney`. `SqlField` (`SetField SqlValue` | `OmitField`) for partial updates via `Db.updateFields` and DEFAULT-omittable INSERTs via `Db.insertFields` (#585) — `OmitField` columns drop from the SQL so the database applies their `DEFAULT`; all-omit → `INSERT … DEFAULT VALUES`. `Db.insertFieldsReturning table fields projection decoder` (#586) appends `RETURNING <projection>` to the same builder and decodes each returned row via `Std.Db.Decode` — for picking up assigned autoincrement ids / applied DEFAULTs / generated columns at INSERT time (SQLite ≥ 3.35 / PostgreSQL).

### `Log` — structured logging

```elm
import Std.Log exposing (println)
import Std.Log as Log

-- Simple println — auto-forced by `let _ =` discard
let
    _ = println "Starting up"
    _ = Log.info "Connection established"
in
    continue

-- Structured (key-value pairs)
Log.infoWith "user logged in" [ "userId", "42", "ip", "1.2.3.4" ]
```

| Function | Type |
|---|---|
| `Log.println` | `String -> Task Error ()` — stdout, no level routing |
| `Log.debug` / `info` / `warn` / `error` | `String -> Task Error ()` |
| `Log.debugWith` / `infoWith` / `warnWith` / `errorWith` | `String -> List a -> Task Error ()` — key/value pairs `[ "k1", v1, "k2", v2, … ]` |

`SKY_LOG_FORMAT` (`plain` | `json`) and `SKY_LOG_LEVEL` (`debug` | `info` | `warn` | `error`) control output format and threshold. Configure defaults in `sky.toml` `[log] format = "json"`. See [Logging precedence](../CLAUDE.md#environment-variable-precedence).

### `Trace` — opt-in application tracing spans

```elm
import Std.Trace as Trace

checkout : Cart -> Task Error Receipt
checkout cart =
    Trace.span "checkout"
        (reserveStock cart
            |> Task.andThen chargeCard
            |> Task.andThen issueReceipt)
```

Tier-1 spans (HTTP request, session load/save, Msg dispatch, DB /
Auth / Http / File operations) are emitted **automatically** by
the runtime — you only reach for `Std.Trace` when you want a named
**application**-level span that groups the auto-spans underneath.

| Function | Type | Notes |
|---|---|---|
| `Trace.span` | `String -> Task e a -> Task e a` | Wrap a Task in a named child span. Parametric in the error type. |
| `Trace.event` | `String -> Task Error ()` | Record an instantaneous event on the current span ("cache miss", "retry"). |
| `Trace.attr` | `String -> String -> Task Error ()` | Tag the current span with a `key = value` attribute (auto-namespaced under `sky.trace.`). |

Spans surface in `/_sky/console`'s Trace tab and export to OpenTelemetry when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. See [observability docs](observability.md) for the full model.

### `Markdown` — render markdown to Std.Ui

```elm
import Std.Markdown as Markdown

view model =
    Ui.column []
        [ Ui.text model.title
        , Markdown.render model.body          -- → Element msg
        ]
```

| Function | Type |
|---|---|
| `Markdown.render` | `String -> Element msg` — block-level (Ui.column of paragraphs / headings / code / lists) |
| `Markdown.renderInline` | `String -> Element msg` — single line of inline-only markdown |

Renders straight into Std.Ui Element trees (no HTML round-trip), so text, headings, lists, blockquotes and images take colour and typography from the surrounding theme. Code blocks, inline code, tables and rules carry a fixed dark palette of their own and will not follow a light theme.

Subset is "chat-grade": headings (`#`-`######`), paragraphs, fenced code, bullet lists (`-` / `*`), ordered lists (any `<digits>. `), horizontal rules (a run of 3+ of `-` / `*` / `_`), blockquotes (`> `), tables (pipe syntax with a `| --- |` separator row), `**bold**` / `*italic*` / `` `code` `` / `[text](url)` / `![alt](url)`.

Deliberate behaviours: an ordered list is renumbered from 1; a code fence's info string (```` ```rust ````) is dropped; a table's alignment colons parse but do not change alignment; emphasis is delimiter-only (no `_underscore_`, no backslash escapes, no `***bold-italic***`, no reference links, no autolinks, no setext `===` headings); a blockquote's lines join into one paragraph and nested `>` levels are not distinguished.

Not supported, **declared with a dated expiry** that `rust/crates/project/tests/declared_stdlib_gaps.rs` enforces — the test goes red on its own when a date arrives: footnotes (needs a two-pass document model), math (needs a formula renderer Std.Ui has no primitive for), mermaid (needs a graph layout engine; ```` ```mermaid ```` renders as an ordinary code block meanwhile), and a hard line break from a trailing double space (needs a Std.Ui line-break primitive that does not exist — note this was *documented as supported* until v0.20.1 and never was). Raw HTML is unsupported **by design and permanently**: the untrusted-input guarantee below is exactly the statement that this parser cannot emit it.

**Safe with untrusted input** — never emits raw HTML or event handlers; every node routes through typed Std.Ui constructors, and a link's URL is neutralised by the renderer, so a `javascript:` / `vbscript:` / non-image `data:` href becomes `about:blank`. (Before v0.20.1 the href was NOT filtered: `[x](javascript:alert(1))` reached the page verbatim, because HTML-escaping does not help against a payload that needs no metacharacter.)

---

## Stdlib quality-of-life batch (v0.15.47)

Seven additions covering the modules every production Sky app
reinvents today. Each ships under the v0.15.46 typed-record
convention — every record carries a `default*` constructor +
`with*` builder helpers so future field additions never break
downstream record literals.

### `Std.Cache` — LRU + TTL in-memory cache

```elm
import Std.Cache as Cache

cfg : Cache.CacheCfg
cfg =
    Cache.withTTL 60000 (Cache.withMaxEntries 10000 Cache.defaultCfg)

usersCache : Task Error (Cache String User)
usersCache = Cache.new cfg

-- ...
Cache.get cache "alice"           -- Task Error (Maybe User)
Cache.put cache "alice" newUser   -- Task Error ()
Cache.stats cache                 -- { hits, misses, evictions }
```

Backed by `hashicorp/golang-lru/v2`. Lazy TTL: expired entries are
pruned on next access (no background goroutine to leak).

### `Std.Email` — Resend / SES / SendGrid / SMTP under one API

```elm
import Std.Email as Email

provider = Email.Resend (System.getenvOr "RESEND_API_KEY" "")

msg = Email.defaultMessage
        { from = "noreply@example.com"
        , to = [ "alice@example.com" ]
        , subject = "Hi"
        }
        |> Email.withTextBody "Hello, world!"

Email.send provider msg     -- Task Error String (provider message ID)
```

`SKY_EMAIL_DRY_RUN=1` short-circuits every provider for unit tests.
`SKY_EMAIL_ENDPOINT_<PROVIDER>` (UPPERCASE) overrides the URL when
pointing at a local mock.

**Attachments** are delivered by all four providers. `withAttachment` adds one;
SMTP and SES send a `multipart/mixed` MIME message (base64, so arbitrary bytes
survive), Resend and SendGrid send the provider's own base64 attachment array.
An attachment with no `mimeType` goes out as `application/octet-stream`. A
message carrying BOTH `withTextBody` and `withHtmlBody` is sent as
`multipart/alternative` — both bodies reach the recipient, and the client picks.

### `Std.Compression` — gzip + zstd

```elm
import Std.Compression as Compression

compressed : Task Error String
compressed = Compression.gzip "large payload"

Compression.zstdCompress payload    -- Task Error String
Compression.zstdDecompress encoded  -- Task Error String
```

`compress/gzip` (stdlib) + `klauspost/compress/zstd`.

### `Std.Image` — backend resize + thumbnail

```elm
import Std.Image as Image

Image.resizeToFit 1600 1600 bytes   -- Task Error Bytes (cap the full image)
Image.thumbnail 400 bytes           -- Task Error Bytes (a small listing image)
Image.dimensions bytes              -- Task Error { width : Int, height : Int }
```

Operates on raw image bytes (`Sky.Core.Bytes`). Preserves the source format
(JPEG stays JPEG at quality 85, PNG stays PNG) and never upscales. Every function
is a `Task` — the module is a backend capability (Go `image/jpeg` + `image/png` +
`golang.org/x/image/draw`), so under `--target web:app` the resize runs on the
server, never in the wasm client. Decode a data-URL upload with
`Encoding.base64Decode` first, then resize and `File.writeFile`.

### `Std.Csv` — RFC 4180 encode/decode + streaming reader

```elm
import Std.Csv as Csv

case Csv.parse "name,age\nAlice,30\nBob,25\n" of

    Ok csv ->
        -- csv.header : List String, csv.rows : List (List String)
        ...

    Err _ ->
        ...

-- Stream a large file row-by-row:
Csv.parseStreamFromFile "users.csv"    -- Task Error (List (List String))
```

### `Sky.Core.Random` — `range`, `weighted`, `shuffle`, seeded\*

```elm
Random.range 1 100              -- Task Error Int (inclusive both ends)
Random.weighted [ (0.7, "a"), (0.3, "b") ]
                                -- Task Error (Maybe a)
Random.shuffle [1, 2, 3, 4, 5]  -- Task Error (List a)

-- Deterministic, reproducible:
s0 = Random.seed 42
( v, s1 ) = Random.seededInt s0 1 100
( f, s2 ) = Random.seededFloat s1
```

Seeded variants thread a `Seed` state via splitmix64 — same seed
produces the same sequence across runs (use for tests and content
generation).

### `String.containsIn` / `startsWithIn` / `endsWithIn` — pipeline-friendly

Haystack-first companions to the existing needle-first helpers:

```elm
"hello world" |> String.containsIn "world"      -- True
"/api/users"  |> String.startsWithIn "/api"     -- True
"image.png"   |> String.endsWithIn ".png"       -- True
```

`String.contains` / `startsWith` / `endsWith` stay for backwards
compatibility.

### `Std.Config` — typed TOML / YAML / JSON decoders

Mirror of `Sky.Core.Json.Decode`'s shape — code that already
decodes JSON gets a consistent vocabulary for TOML and YAML:

```elm
import Std.Config as Config

dbDecoder : Decoder DbCfg
dbDecoder =
    Config.field "host" Config.string
        |> Config.andThen (\h ->
            Config.map (\p -> { host = h, port = p })
                (Config.field "port" Config.int))

Config.loadFromFile "config/database.toml" dbDecoder
    -- Task Error DbCfg (extension dispatch: .toml/.yaml/.yml/.json)
```

TOML via `BurntSushi/toml`, YAML via `gopkg.in/yaml.v3`, JSON via
the stdlib `encoding/json`.

---

## Naming-consistency surface (v0.15.48)

Three additive batches improving discoverability without disturbing
any existing public types or function names.

### `Sky.Core.ToString` — uniform `fromX` naming

```elm
import Sky.Core.ToString as ToString

ToString.fromInt   42      -- "42"   — routes to String.fromInt
ToString.fromFloat 3.14    -- "3.14" — routes to String.fromFloat
ToString.fromBool  True    -- "True"
ToString.fromTime  ms      -- canonical Time.timeString
```

Zero runtime overhead — the bindings are tail-call aliases to the
existing kernels. The point is editor + `sky doc` discoverability:
AI-written code is encouraged to default to `ToString.fromInt n`
rather than memorising which sub-namespace each type lives under.
The canonical kernel-direct call (`String.fromInt`, `Time.timeString`)
stays available for code that prefers the explicit shape.

### `Std.Auth.signTokenWithClaims` / `verifyTokenWithAlgorithm`

The arity-3 `Auth.signToken : String -> a -> Int -> Result Error String`
shape stays canonical for the simple secret + claims + expiry case.
For richer JWT shapes, reach for the typed-builder companion:

```elm
import Std.Auth as Auth
import Sky.Core.Jwt as Jwt

token : Result Error String
token =
    Auth.signTokenWithClaims
        (Jwt.rs256 privateKeyPem)
        (Jwt.claims
            |> Jwt.subject "user-42"
            |> Jwt.audience "https://api.example.app"
            |> Jwt.expiresAt (now + 86400)
            |> Jwt.jwtId tokenId
            |> Jwt.withClaim "scope" "admin"
        )

verified : Result Error String   -- raw JSON claims string
verified = Auth.verifyTokenWithAlgorithm (Jwt.hs256 (Secret.fromString "secret")) now token
```

### `Std.Time` `*Utc` infallible companions

Every zone-aware `String -> Int -> Result Error _` ships a `Int -> _`
UTC variant for server-internal timestamp work that doesn't need
TZ-awareness:

| Zone-aware (`String -> Int -> Result Error _`) | UTC infallible (`Int -> _`) |
|---|---|
| `year` / `month` / `day` | `yearUtc` / `monthUtc` / `dayUtc` |
| `dayOfWeek` / `dayOfYear` / `weekOfYear` | `dayOfWeekUtc` / `dayOfYearUtc` / `weekOfYearUtc` |
| `isWeekend` | `isWeekendUtc : Int -> Bool` |
| `startOfDay` / `endOfDay` | `startOfDayUtc` / `endOfDayUtc` |
| `startOfWeek` / `startOfMonth` / `endOfMonth` | `startOfWeekUtc` / `startOfMonthUtc` / `endOfMonthUtc` |
| `startOfYear` / `endOfYear` | `startOfYearUtc` / `endOfYearUtc` |

The UTC variants plug `"UTC"` (always-valid IANA zone) at the call
site, so the `Result Error _` wrap collapses to the bare value.
Reach for them in logs / audit rows / server-internal timestamp
arithmetic. For user-facing UI, keep using the zone-aware form.

---

## Arity-0 consistency surface (v0.15.50)

Pre-v0.15.50 the stdlib was inconsistent about whether
arity-0 helpers took `()`:

| Convention | Examples |
|---|---|
| Takes `()` | `Time.now ()`, `Time.unixMillis ()`, `System.cwd ()`, `System.args ()`, `Io.readLine ()`, `Db.connect ()` |
| Bare | `Uuid.v4`, `Uuid.v7` |

For new code preferring a uniform `Pure.foo ()` shape, reach for
`Sky.Core.Pure`. Every entry is a typed `() -> Task Error a`
companion that re-routes to the canonical kernel — same runtime
performance, but one consistent call shape:

```elm
import Sky.Core.Pure as Pure
import Sky.Core.Task as Task
import Std.Log exposing (println)

main =
    Pure.systemCwd ()
        |> Task.andThen (\cwd  -> Pure.uuidV4 ())
        |> Task.andThen (\uuid -> Pure.timeNow ())
        |> Task.andThen (\now  -> println (String.fromInt now))
```

Full Pure.* surface (9 entries):

```
Pure.uuidV4         : () -> Task Error String
Pure.uuidV7         : () -> Task Error String
Pure.timeNow        : () -> Task Error Int
Pure.timeUnixMillis : () -> Task Error Int
Pure.systemArgs     : () -> Task Error (List String)
Pure.systemCwd      : () -> Task Error String
Pure.systemLoadEnv  : () -> Task Error ()
Pure.ioReadLine     : () -> Task Error String
Pure.dbConnect      : () -> Task Error Db
```

Inclusion criterion: a stdlib binding belongs to `Sky.Core.Pure`
when (a) it takes no real Sky-level argument that disambiguates
the call AND (b) it returns a `Task Error a` — i.e. entropy /
clock / env / I/O / database-connection surfaces where the
inconsistency bit users most often. Non-zero-arg helpers like
`Random.int`, `Crypto.randomToken`, `System.exit`, `Process.run`
are NOT candidates — their argument list carries semantic
information.

Existing names + shapes are **unchanged** (per the v0.15.44
backwards-compat lesson). `Pure.*` is purely additive — call
sites preferring the legacy convention keep working exactly as
before.

---

## Web modules

### `Server` — Sky.Http.Server

```elm
import Sky.Http.Server as Server

main =
    Server.listen 8000
        [ Server.get "/" (\_ -> Task.succeed (Server.text "Hello!"))
        , Server.get "/api/users/:id" getUser
        , Server.post "/api/data" handlePost
        , Server.static "/assets" "./public"
        ]
```

Routing: `get`, `post`, `put`, `delete`, `any`, `static`, `group` (prefix), `use` (middleware), `listen`.

`static` serves a directory (path-traversal-safe, with MIME detection,
`Last-Modified`, and `Range` support). Compressible assets — `.wasm`, JS, CSS,
JSON, SVG, and any `text/*` — are **gzipped on the wire automatically** for a
client that sends `Accept-Encoding: gzip` (a plain `GET`, `200`, no `Range`);
already-compressed media (images, video, fonts) and partial/`206` responses pass
through untouched. This matters most for a Sky.Spa client `.wasm`: a standard-Go
wasm bundle is multi-MB raw but ~¼ of that gzipped, and the browser downloads the
raw bytes unless the server compresses. `Vary: Accept-Encoding` is always set so
shared caches key on it. The same compression applies to the Sky.Live and
Sky.Webview static mounts.

Extractors (Layer 3 Sky source — `Sky.Http.Server.sky`): `param`
(path `:id`), `queryParam`, `header`, `getCookie`. Kernel-side
extras: `formValue`, `body`, `path`, `method`.

Responses: `text`, `json`, `html`, `withStatus`, `redirect`,
`cookie`, `withCookie`, `withHeader`.

### `Live` — Sky.Live (server-driven UI)

Sky.Live is the **server-driven UI backend** — a TEA loop over an SSE wire, with
sessions, routing, and a shared store. You do not import it directly: write the
app with [`Std.App`](skyapp/overview.md) and `App.run` picks Sky.Live on the
default `--target web` (and `--target desktop` for a native window).

```elm
import Std.App as App

appDef =
    App.app
        { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withRoutes [ App.route "/" HomePage ]
        |> App.withNotFound HomePage

main : Task Error ()
main =
    App.run appDef
    -- Optional config attaches to the same builder:
    -- `|> App.withHead …` / `App.withGuard …` / `App.withConfig …` / `App.withBase …`.
```

**Start and stop an app from a Task program (v0.27).**
`App.serve app : Task Error App.Running` starts the web app embedded (no signal
handler, no process exit) and succeeds once it is listening;
`App.address running` is the bound `host:port` (port `0` picks a free one);
`App.stop running : Task Error ()` stops it gracefully (live streams close,
in-flight requests get 5 seconds, sessions end, the store closes, the port is
free). `Live.serve` / `Live.address` / `Live.stop` are the `Std.Live` forms.
Two apps in one process keep their own sessions and store; see
[embedded Sky.Live](skylive/embedded.md#several-apps-in-one-process).

**Sessions without cookies (v0.27).** `App.withSessionTransport HeaderToken`
(`Live.withSessionTransport "header"`, or `SKY_LIVE_SESSION_TRANSPORT=header`)
carries the session in the `X-Sky-Session` header instead of the `sky_sid`
cookie, for hosts that cannot keep cookies. Sky.Live only. See
[the security model](skylive/architecture.md#sessions-without-cookies-the-header-transport).

`App.withNotFound` is mandatory for web (a compile-enforced fallback flag), and
routes are built with `App.route` / `App.routeParam` / `App.api`. See
[Sky.Live overview](skylive/overview.md) for the full TEA flow that this target
delivers — the low-level `Sky.Live` runtime is documented there as the mechanism
`--target web` compiles to.

#### `Std.Live.Head` — per-page `<head>` injection (v0.15.58+)

Optional per-page `<head>` injection — attach via `App.withHead`
(a `model -> List (Html msg)`; it lowers to the runtime's `Live.withHead`). Runtime renders the list and splices it into `<head>`
on every full GET, after the runtime's required baseline meta
tags and before the inline `<style>` reset. Absent field → empty
insert (byte-identical to pre-v0.15.58 output).

```elm
import Std.Live.Head as Head

headFor model =
    [ Head.title (titleFor model.page)
    , Head.meta "description" (descriptionFor model.page)
    , Head.canonical (canonicalFor model.page)
    , Head.metaProperty "og:title" (titleFor model.page)
    , Head.themeColor "#1a1a2e"
    , Head.jsonLd (jsonLdFor model.page)
    ]
```

Helpers (all return `Html msg`):

| Helper | Emits |
|---|---|
| `title : String -> Html msg` | `<title>…</title>` |
| `meta : String -> String -> Html msg` | `<meta name="…" content="…">` |
| `metaProperty : String -> String -> Html msg` | `<meta property="…" content="…">` (Open Graph, Facebook) |
| `link : List (String, String) -> Html msg` | `<link …>` with arbitrary attrs (preload, favicons, …) |
| `canonical : String -> Html msg` | `<link rel="canonical" href="…">` |
| `jsonLd : String -> Html msg` | `<script type="application/ld+json">…</script>` (raw JSON) |
| `themeColor : String -> Html msg` | `<meta name="theme-color" content="…">` |
| `rss : String -> String -> Html msg` | `<link rel="alternate" type="application/rss+xml" …>` |

SSE patches scope to `<body>`, so head updates require a full
reload — fine for the typical case (head depends on page identity,
which changes via sky-nav navigation that already does a full-body
fetch + history push).

### `Std.Webview` — desktop UI backend (v0.16+)

The desktop backend behind `App.run` on `--target desktop:<os>` — same TEA shape
(init / update / view / subscriptions) as every other target, and the runtime
opens a native window (WKWebView on macOS in v0.1; Linux + Windows in v0.2). No
HTTP server, no SSE, no session store — the bridge is in-process `Bind` + `Eval`
via `webview_go`. You do not import `Std.Webview` directly; write the app with
[`Std.App`](skyapp/overview.md) and pick the desktop target at build time.

```elm
import Std.App as App

appDef =
    App.app
        { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withWindow "Sky Stopwatch" 800 600
        |> App.withNotFound ()

main : Task Error ()
main =
    App.run appDef
```

```bash
sky build --target desktop       # Sky.Live in a native window
sky build --target desktop:mac   # native webview shell (Std.Webview backend)
```

`WindowCfg` v0.1 is `{ title : String, size : (Int, Int) }`; v0.2
reopens for `alwaysOnTop` / `transparent` / `decorated` + adds tray
icons, native file dialogs, global hotkeys, and Linux + Windows smoke
validation.  Build it via `defaultWindow` + `withTitle` + `withSize`
builders so future field additions stay source-compat.

`sky build` auto-detects Sky.Webview projects and flips `CGO_ENABLED=1`
on the first build pass; the stub `runtime-go/rt/webview_stub.go`
covers `!cgo || !darwin` so non-macOS builds link cleanly and surface
a runtime `Err Error` on call.  Example: `examples/31-webview-stopwatch-ui`.

### `Std.Native` / `Std.Bundle` — device capabilities and native packaging

`Std.Native` is the device from a Sky.Spa client (web, or the `mobile:ios`,
`mobile:android` and `desktop:mac` shells). Each capability is a `Task Error a`
run through `Cmd.perform`, so a denial or an absent API is an `Err`, never a
crash: `geolocation`, `clipboardWrite` / `clipboardRead`, `vibrate`, `share`,
`storageSet` / `storageGet` / `storageRemove` (`localStorage`), `isOnline`,
`language`, `setTitle`, `prefersDarkMode`, `openUrl`, `notify`,
`batteryStatus`, `pickFile` / `pickImage` / `capturePhoto`, and `bridge` for a
capability the app registers itself.

v0.27.0 adds the device's secure store and its biometric prompt:

```elm
-- doc-example: skip  (signatures)
secureSet : String -> Secret -> Task Error ()          -- Keychain / Android Keystore AES-GCM
secureGet : String -> Task Error (Maybe Secret)
secureRemove : String -> Task Error ()
authenticate : String -> Task Error Bool               -- Face ID / Touch ID / BiometricPrompt
```

With no native shell (a browser, a server) they are `Err Unavailable`; the
secure store never falls back to `localStorage`. `authenticate` is `Ok True`,
`Ok False` (no match), `Err PermissionDenied` (cancelled) or `Err Unavailable`.

It also adds the camera code scanner:

```elm
-- doc-example: skip  (signatures)
type CodeFormat = Qr | Aztec | DataMatrix | Pdf417 | Ean8 | Ean13 | UpcE | Code39 | Code93 | Code128 | Itf | Codabar
type alias ScanOptions = { formats : List CodeFormat, prompt : String }
type alias ScannedCode = { format : CodeFormat, text : String }
scanCode : ScanOptions -> Task Error (Maybe ScannedCode)   -- VisionKit / camera + ZXing
```

`Ok (Just code)` is a code of one of the asked formats (an empty list asks for
all of them), `Ok Nothing` means the user closed the scanner, `Err
PermissionDenied` means the camera was refused, and `Err Unavailable` means
there is no camera scanner (a browser, the desktop window, a server, the iOS
simulator). A UPC-A code is reported as `Ean13`.

`Std.Bundle` declares what the native shells ship with: identity (`withId`,
`withName`, `withVersion`, `withBuild`, `withIcon`), assets, permissions with
their purpose strings (`withPermission`, `withUsage Bundle.Camera "…"`) and
typed Apple entitlements (`withEntitlement (Bundle.AppGroup "group.…")`). A call
to `Native.authenticate`, `capturePhoto`, `geolocation` or `scanCode` without
its permission fails the iOS and Android builds. `sky package --release` makes the
store artefact. Full guide: [`docs/skyapp/native.md`](skyapp/native.md).

### `Event` — typed DOM event bindings (`Std.Html.Events`)

v0.13: `Std.Html.Events` (renamed from `Std.Live.Events`). Each builder
returns an `Attribute msg` carrying a typed `Event msg`, so the compiler
flags a handler-shape mismatch (`onInput` bound to a `msg` instead of a
`String -> msg`) at the call site. `onClick`, `onInput`, `onChange`,
`onSubmit`, `onFocus`, `onBlur`, `onMouseOver`, `onMouseOut`, `onKeyDown`,
`onKeyUp`, `onKeyPress`, `onCheck`, `onImage` (with `fileMaxWidth` /
`fileMaxHeight` / `fileMaxSize`), `onFile`, `on` (generic escape hatch).

### `Html` — HTML elements

v0.13: a typed Sky-source stdlib module. ~75 element builders returning
the typed `Html msg` ADT (`text`, `div`, `span`, `p`, `h1`-`h6`, `a`,
`button`, `input`, `form`, `table`, `tr`, `td`, …). `render : Html msg
-> String` for server-side rendering; `raw` for trusted un-escaped HTML.

### `Attr` — HTML attributes (`Std.Html.Attributes`)

v0.13: ~60 builders returning the typed `Attribute msg` ADT, so the
compiler rejects `disabled "yes"` / `rows "five"`. String-valued
(`class`, `id`, `href`, `src`, `style`, …), Int-valued (`rows`, `cols`,
`width`, `height`, `tabindex`, …), Bool-valued (`checked`, `disabled`,
`required`, `readonly`, `autofocus`, …). `type_` (keyword clash with
`type`). `attribute` / `dataAttribute` / `boolAttribute` escape hatches;
`none : Attribute msg` for the False branch of a conditional attr.

### `Css` — typed stylesheets

v0.13: a typed Sky-source stdlib module — typed where the value space
is bounded, `String` + `rawProp` escape hatch where it is not.

```elm
import Std.Css as Css

myStyles =
    Css.stylesheet
        [ Css.rule ".btn"
            [ Css.display Css.Flex          -- keyword enum
            , Css.padding (Css.rem 0.5)     -- Length
            , Css.background (Css.hex "3b82f6")  -- Color
            , Css.color (Css.hex "ffffff")
            , Css.cursor Css.Pointer
            ]
        ]
```

`Length` ADT (`px`, `rem`, `em`, `pct`, `vh`, `vw`, `ch`, `fr`, `num`,
`zero ()`, `auto ()`, `lengthRaw`, `calc`, `minmax`), `Color` ADT
(`hex`, `rgb`, `rgba`, `hsl`, `hsla`, `transparent ()`, `currentColor
()`, `colorRaw`), keyword enums (`Display`, `Position`, `Cursor`,
`FontWeight`, `FlexDirection`, `Align`, `Overflow`, …). Open-ended
compound properties (`transition`, `transform`, `gridTemplateColumns`,
`fontFamily`, `border`, …) take a `String`. `rule` / `media` /
`keyframes` / `stylesheet` / `styles` (inline) / `property` / `rawProp`.

> Bare keyword constants (`Css.zero`, `Css.auto`, `Css.none`,
> `Css.transparent`) take `()` to sidestep zero-arity memoisation —
> write `Css.margin (Css.zero ())`. See [Limitation 13](../CLAUDE.md#active-limitations).

### `Ui` — typed no-CSS layout DSL

A typed layout DSL. Build a UI from typed primitives and typed attributes — Sky.Ui renders to inline-styled HTML on the server side and Sky.Live's wire ferries diffs to the browser. **No CSS files**, no template languages, no client framework.

```elm
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Border as Border
import Std.Ui.Font as Font

view model =
    Ui.layout []
        (Ui.row
            [ Ui.spacing 12, Ui.padding 16
            , Background.color (Ui.rgb 255 102 0)
            , Font.color (Ui.rgb 255 255 255)
            , Border.rounded 4
            ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.el [ Font.size 24, Font.bold ] (Ui.text (String.fromInt model.count))
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ])
```

Layout primitives: `el / row / column / wrappedRow / grid / paragraph / textColumn / text / textNoWrap / none / button / input / form / link / image / html / layout` (v0.27.0: `text` is inline inside a `paragraph` and its own wrapping box elsewhere, so two texts in a column are two lines; `textNoWrap` keeps one line) (`wrappedRow` lets children wrap to a new line via `flex-wrap: wrap`; `grid` is CSS-Grid auto-fit — set min column width via `Ui.gridColumns N`, use this NOT `wrappedRow` when children contain `<img>` because flex-wrap collapses to 1-per-row in that case). Length: `px Int / fill (bare) / fillPortion Int / content / shrink / minimum Int Length / maximum Int Length / vh Int / vw Int` (`vh` / `vw` are viewport-relative — useful for `Ui.height (Ui.vh 100)` shells). Padding: `padding / paddingXY / paddingEach / spacing`. Alignment: `centerX / centerY / alignLeft / alignRight / alignTop / alignBottom / pointer`. Overflow: `clip / clipX / clipY / scrollbars / scrollbarX / scrollbarY`. Nearby (overlays): `above / below / onLeft / onRight / inFront / behind`. Attributes: `width / height / style / class / htmlAttribute / name`. Events: `onClick / onSubmit / onInput (typed String→msg) / onChange / onFocus / onMouseOver / onMouseOut / onKeyDown / onFile / onImage`. File hints: `fileMaxSize / fileMaxWidth / fileMaxHeight`. Colour: `rgb / rgba / white / black / transparent`. Widget islands: `island { name, id, props } attrs` places an element a third-party JS widget owns (registered from a same-origin script with `window.Sky.island`), and `onIslandEvent type decoder toMsg` decodes the widget's events into typed Msgs; `Std.Html.island` / `Std.Html.Events.onIslandEvent` are the `Std.Html` forms, and `Cmd.toIsland` sends commands back (see [Widget islands](skyui/overview.md#widget-islands--third-party-js-widgets)).

Sub-modules:
- **`Std.Ui.Background`** — `color / image url / linearGradient angle stops / gradient css`
- **`Std.Ui.Border`** — `color / width / widthEach {top, right, bottom, left} / rounded / solid / dashed / dotted / shadow {offsetX, offsetY, blur, spread, color} / glow blur color / innerShadow {…}`
- **`Std.Ui.Font`** — `color / family / size / weight / bold / semiBold / regular / light / extraBold / black / italic / underline / noDecoration / lineThrough / overline / letterSpacing em / wordSpacing em / alignLeft / alignRight / alignCenter / center / justify / sansSerif / serif / monospace`
- **`Std.Ui.Region`** — `heading n` / `mainContent` / `navigation` / `footer` / `aside` / `label text` / `announce` / `announceUrgently` (the renderer dispatches `<h1>`..`<h6>` / `<main>` / `<nav>` / `<footer>` / `<aside>` from the Description, and emits `aria-label` / `aria-live` for the rest)
- **`Std.Ui.Input`** — typed form controls: `button / text / multiline / email / username / search / currentPassword {show: Bool} / newPassword {show: Bool} / checkbox / radio {options, selected, …} / radioRow {…} / slider {min, max, step, value, …}` + `option value labelEl` (RadioOption ctor) + `labelAbove / labelBelow / labelLeft / labelRight / labelHidden / placeholder`
- **`Std.Ui.Lazy`** — `lazy / lazy2..lazy5`. **Real memoisation, not no-op wrappers.** The Sky bodies (`sky-stdlib/Std/Ui/Lazy.sky`) are kernel-mapped (`rust/crates/lower/src/kernel.rs:583-587`) to a bounded LRU in `runtime-go/rt/lazy.go`, keyed on the function pointer plus an injective fingerprint of each argument. Four things to know before reaching for it:
  - **A hit saves only your `Element` construction.** The cache holds the `Element` ADT (`Lazy.sky:25` returns `Element msg`), not rendered HTML — so a cache hit still pays `renderElement`, `HtmlToVNode`, `assignSkyIDs`, the four style walks and `renderVNode`. See `docs/perf/skylive-interaction-cost.md` → "Every pass over the tree".
  - **The key costs a full reflective deep walk, on hits as well as misses.** `lazyKey` (`rt/lazy.go:148`) calls `identityKey` (`rt/identity_key.go`) per argument, which reflectively walks the whole value (depth-bounded at 512). For a large argument the key can cost more than rebuilding the subtree.
  - **The LRU is process-wide, behind one mutex, capped at 1024 entries** (`rt/lazy.go:44-49`; override with `SKY_UI_LAZY_CAP`). It is shared across every session, so a busy multi-session app can evict one session's entries with another's.
  - **The function is keyed by pointer address** (`%p`, `rt/lazy.go:150`). A stable top-level binding — `Lazy.lazy renderItem item` — is the intended shape and hits. A locally-constructed closure gets a fresh address each render and will never hit.
- **`Std.Ui.Keyed`** — `keyed` (emits `sky-key` for diff identity)
- **`Std.Ui.Responsive`** — `classifyDevice / adapt {phone, tablet, desktop}`
- **`Std.Ui.Canvas`** — typed 2D scenes (v0.27.0). `scene { width, height, label } shapes` / `sceneWith attrs cfg shapes` with `rect / circle / ellipse / line / polyline / polygon / path (List PathCommand) / text / group`, paint (`fill / noFill / stroke / strokeWidth / opacity / fontSize / anchorStart / anchorMiddle / anchorEnd`), transforms (`translate / rotate / scale`) and events (`onClick msg`, `onPointerDown / onPointerMove / onPointerUp : (Point -> msg)` in scene units). SVG on Sky.Live, Sky.Spa and the desktop window; Braille cells on Sky.Tui. See [Canvas](skyui/overview.md#canvas--typed-2d-scenes-stduicanvas).
- **`Std.Ui.Terminal`** — an interactive terminal element bound to a `Process.withPty` child (v0.27.0): `init id`, `attach toMsg process`, `update toMsg msg`, `view toMsg terminal attrs`. The runtime emulates the terminal on the server and a built-in widget island draws screen-diff frames on a canvas (`Process.screen` + `Cmd.toIsland`; a transparent text layer keeps selection, copy and screen readers); a remount is repainted from the screen and its scrollback. Sky.Live and desktop; refused on Sky.Spa targets. See [Terminal](skyui/overview.md#terminal--a-pty-in-the-page-stduiterminal).
- **`Std.Ui.Chart`** — typed chart primitives (v0.16.0). `line / area / bar / sparkline / heatmap` accept typed `Series` records ({label, color, points : List Point}) and render to inline-styled SVG. Pair with `Ui.layoutWith` to embed in dashboards (used by the bundled Sky Console + `examples/26-ui-showcase`). XSS-hardened: all axis ticks + tooltip labels HTML-escape through the same renderer as text content; no innerHTML, no `data-sky-eval`.
- **`Std.Ui.Animation`** — typed CSS keyframe animation DSL (v0.15.57). Build via `defaultSpec "fadeIn" |> withDuration 500 |> withEasing easeOut |> withKeyframes [(0, [Transform.opacity 0.0]), (100, [Transform.opacity 1.0])]` + `Animation.attribute spec`.  Auto-wrapped in `@media (prefers-reduced-motion: no-preference)` by default; opt out via `withRespectReducedMotion False` only when motion is semantically required (loading spinner, progress indicator).
- **`Std.Ui.Transition`** — typed CSS transitions (v0.15.57). `Transition.attribute [property "background-color", duration 200, easing easeOut]` pairs with `Background.hoverColor` so the browser animates between base + `:hover` states.  Also auto-wrapped in the reduced-motion guard.  Use `attributeUnsafe` to opt out.
- **`Std.Ui.Transform`** — typed `transform:` property helpers used by Animation/Transition (`translateX / translateY / translate / scale / scaleXY / rotate / skewX / skewY / opacity`).  All `transform`-shaped helpers join into ONE `transform:` shorthand per keyframe; `opacity` emits standalone.
- **`Std.Ui.Grid`** — explicit CSS-grid track lists (v0.15.57). Typed `Track` ADT (`fr / px / auto / minContent / maxContent / minmax / repeat / repeatAutoFit / repeatAutoFill`) + `Grid.columns / Grid.rows / Grid.tracks`.  Use when `Ui.gridColumns N` (auto-fill) is too coarse — e.g. sidebar layouts (`[fr 1, px 200, fr 1]`), content-aware columns (`[auto, fr 1]`), responsive card grids (`[repeatAutoFit (minmax (px 240) (fr 1))]`).

**Best-practice for forms with sensitive inputs (passwords, API keys):** wrap inputs in `Ui.form` and dispatch on `onSubmit DoSignIn` with a typed record. Do NOT wire `onInput` on the password field — that would dispatch the secret on every keystroke into Model and through every session-store write. See [Sky.Ui overview](skyui/overview.md#forms--the-password-best-practice-pattern) for the full pattern.

**File / image upload:** on Sky.Live and the desktop window `Ui.onImage` resizes to `fileMaxWidth × fileMaxHeight` (default 1200×1200) and re-encodes as JPEG @ 0.85 quality before sending; on Sky.Spa (`web:app`) it sends the image unchanged, so resize on the server with `Std.Image.resizeToFit`. `Ui.onFile` ships the raw data URL. Both honour `fileMaxSize` for client-side caps. See [Sky.Ui overview](skyui/overview.md#file--image-upload).

Full reference, surface-coverage table, known limitations: [Sky.Ui overview](skyui/overview.md).

### `RateLimit` — request throttling

```elm
import Sky.Http.RateLimit as RateLimit

if RateLimit.allow "login" clientIp 5 1 then
    handleLogin req
else
    Task.succeed (Server.withStatus 429 (Server.text "too many attempts"))
```

`allow : String -> String -> Int -> Int -> Bool` — try to consume
one token from a token-bucket keyed by `(name, key)`. Arguments
are `name` (limiter label), `key` (typically the client IP),
`capacity` (bucket size), `refillPerSec` (refill rate). Returns
`True` when the request is allowed, `False` when the bucket is
empty. For declarative wiring use `Middleware.withRateLimit`.

### `Middleware` — composable handler wrappers

Each helper returns a decorated `Handler` — compose by chaining
with `|>` or by nesting via `Server.use`.

| Helper | Signature |
|---|---|
| `withCors` | `List String -> Handler -> Handler` — allowed-origin list |
| `withLogging` | `Handler -> Handler` — `method path status duration` to stdout |
| `withBasicAuth` | `String -> String -> Handler -> Handler` — `username password handler` |
| `withRateLimit` | `String -> Int -> Int -> Handler -> Handler` — `key requestsPerWindow windowSeconds handler` (per-IP fixed window) |

```elm
import Sky.Http.Middleware as Middleware

Server.use Middleware.withLogging
    (Server.use (Middleware.withRateLimit "api" 100 60)
        [ Server.get "/api/users" listUsers
        , ...
        ]
    )
```

`Server.use middleware routes` wraps the handler of every route in the list
(a static route passes through unchanged). Before v0.27 it returned the
routes unchanged and the middleware did nothing.

#### Response headers, CORS and the CSRF 403

`Server.withHeader name value response` decorates the response a handler
returns, and nothing else. It returns a new response: a base value shared by
two routes is never changed. Header names are case-insensitive, so
`withHeader "x-a"` after `withHeader "X-A"` leaves one header with the last
value. A response the runtime writes itself never passes through the handler,
so it carries none of the handler's headers: the CSRF 403, a 404 or 405 from
the router, and the dev Host-guard 403.

`Middleware.withCors origins handler` does two things. It answers a preflight
`OPTIONS` with `204` and `Access-Control-Allow-Origin` / `-Methods` /
`-Headers: Content-Type, Authorization`, without calling the handler, and it
adds `Access-Control-Allow-Origin` to the handler's response. A preflight
reaches the wrapped handler because a path with one route answers every method
there. On a path with two routes (`Server.get "/p"` and `Server.post "/p"`),
the preflight goes to the route whose method the browser names in
`Access-Control-Request-Method`, so wrap that route (normally both).

A cross-origin `POST` / `PUT` / `DELETE` / `PATCH` with no `Authorization`
header is refused by the CSRF guard before the handler runs. That 403 has no
CORS headers, so the browser reports a CORS error rather than a 403. The
cross-origin page cannot read this server's `__sky_csrf` cookie, so it can
never send the token (this is why `withCors` does not allow the `X-Sky-Csrf`
header). Authenticate a cross-origin call with an `Authorization` header,
which the CSRF guard exempts, or register the route with `Server.api`.

#### CSRF protection + JSON/API clients

Every `Server.listen` server wraps its routes in CSRF protection
**by default** (on for `POST` / `PUT` / `DELETE` / `PATCH`). A
cookie-session browser form works automatically — the runtime issues
a `__sky_csrf` cookie and Sky.Live's JS echoes it in an `X-Sky-Csrf`
header (HTML forms get a hidden `__sky_csrf` field auto-injected).

A **machine / API client** that has no CSRF token gets a `403` with a
JSON body naming the escape hatches. Three ways to call a mutating
endpoint from a non-browser client:

- **Send an `Authorization` header** (Bearer / Basic). Such a request
  is auto-exempt: CSRF only guards *ambient-cookie* browser requests,
  and a browser never auto-attaches `Authorization`, so it can't be a
  CSRF vector. This is the recommended path for token-authenticated
  JSON APIs — no config needed.
- **`SKY_CSRF=off`** — disables CSRF for the whole server (pure-API
  services with their own auth).
- **`Server.api "POST /webhooks/stripe" handler`** — register the route
  as an API route. It is exempt for the method its spec names only:
  `Server.api "GET /report"` does not exempt `POST /report`. A spec with
  no method (`Server.api "/hook"`) exempts every method. `Live.api`
  follows the same rule.

Cookie-session POSTs (no `Authorization` header) stay fully protected
in every case.

A JSON endpoint that authenticates with the browser's session COOKIE is
not an API route. Register it with **`Server.rpc "POST /path" handler`**.
It needs no CSRF token (a wasm or JS client that cannot read the HttpOnly
CSRF cookie can still call it). In place of the token, the runtime refuses
the request before the handler runs unless:

- the method is the route's method (405 otherwise);
- the body is `Content-Type: application/json` (a cross-origin JSON POST
  always needs a CORS preflight, which the server does not grant);
- `Sec-Fetch-Site` is `same-origin` (or `none`), or the `Origin` header
  equals the app's public origin. `Origin: null` is refused. The public
  origin is `SKY_PUBLIC_URL` when set (one URL or a comma-separated
  list), else the request's scheme (TLS or `X-Forwarded-Proto`) and
  `Host`. A request with neither `Origin` nor `Sec-Fetch-Site` is not
  from a browser and passes with the JSON body.

The 403 body names `SKY_PUBLIC_URL`. Set it when a proxy rewrites the
`Host` header, or behind a tunnel. The Sky.Spa auto-split registers every
`/_rpc/<Msg>` with `Server.rpc`.

---

## Product analytics — `Std.Analytics`

Typed product analytics: page views, actions, and e-commerce events with
**typed property values**. The differentiator vs a stringly-typed SDK is
that a prop's VALUE is Sky-typed — an `Int` is an `Int`, `Money` is
lossless (never a float), and identity is a distinct `Pii` type the
pipeline can redact by construction, never a stray `String`.

```elm
import Std.Analytics as Analytics

-- open payload builder: any code (your app OR a library) emits without
-- coupling to a central union
Analytics.track
    (Analytics.event "product_viewed"
        [ Analytics.string "sku" "SKU-42"
        , Analytics.money "price" price
        , Analytics.pii "email" (Analytics.piiEmail user.email)  -- redacted
        ])

-- or derive the payload from your OWN typed event union (no encoder):
Analytics.trackEvent (Purchased { orderId = id, total = cart })
```

**Identity + consent.** Consent defaults to **`Granted`** (v0.19.1) — enabling
analytics captures fully and `identify user.id traits` attaches the user. This
is the DX-friendly default; a privacy-conscious app shows a consent banner and
downgrades with `setConsent Anonymous` (random anon id, no identity) or
`setConsent Denied` (drops all capture). Consent + identity are session-scoped,
so one Sky.Live user's identity never bleeds into another's.

**Auto page-views (opt-in).** This attaches on the low-level `Sky.Live` backend
config (the mechanism `--target web` compiles to): `|> Live.withAnalytics {
pageViews = True }` — every full page load is captured (consent-gated), with
anonymised device + IP context. Add an **`identify` resolver** to attribute an
already-authenticated session (including the first render, before any Msg runs)
without a manual `identify` call — add the `withAnalyticsIdentify` builder:

```elm
|> Live.withAnalytics { pageViews = True }
|> Live.withAnalyticsIdentify (\model -> Maybe.map .id model.currentUser)  -- model -> Maybe String
```

The runtime resolves it against the model on each page-view and it is the session's
identity authority — symmetric by design: `Just id` stamps the session user id, and
`Nothing` / `Just ""` **clears** it, reverting the session to anonymous (the cleared
state persists on the next render). So when a session signs out
(`model.session` → `Nothing` → resolver returns `Nothing`), subsequent auto
page-views are anonymous again rather than continuing to attribute to the
signed-out user. It's the app's explicit opt-in for attributing the identity it
already holds.

**Sinks + store.** `configure [ StderrSink, FileSink "events.jsonl",
Custom (\line -> Http.post collector line) ]` fans every event to your
destinations. A SQLite/Postgres store persists events — it reuses the console DB by
default, or a `[analytics] dbPath` override in `sky.toml`. `erase id`
(right-to-erasure) + `totalEvents` / `uniqueUsers` / `eventCounts` /
`recentEvents` back an admin view; the Sky Console's **Analytics** tab
renders totals, per-event counts, the recent stream (a `page_view` shows its
`props.path`, e.g. `page_view  /shop/necklaces`), and revenue grouped
by currency. Full API + per-binding docs: `sky doc Std.Analytics`. Worked
example: `examples/52-blog-analytics`.

**Query on Store (v0.19.2).** The read / query / aggregate / patch side of the
analytics store is plain `Std.Db.Store` — only the consent-gated WRITE (`track`)
stays in the runtime. Query the stored events with the same typed Store API as any
other table:

- **`Analytics.eventsStore : Store AnalyticsEvent`** — a Store over the
  `analytics_events` table. `AnalyticsEvent` is the envelope record with typed
  columns `id` / `ts` / `event` / `userId` / `anonymousId` plus the open metadata
  bag `props` (event props JSON) and device `context` (JSON).
- **`Analytics.openStore : () -> Task Error Db`** — a connection to the analytics
  store (the console DB, or the `[analytics] dbPath` override), for use with
  `eventsStore`. Query the envelope columns directly; reach for `Store.selectRaw`
  + `json_extract` / `->>` for the JSON `props` — same on SQLite and Postgres.
- The built-in aggregates `totalEvents` / `uniqueUsers` / `eventCounts` /
  `recentEvents` are now plain `Std.Db.Store` queries over `eventsStore` (Sky, not
  Go kernels). **Breaking (v0.19.2):** `recentEvents` now returns
  **`List AnalyticsEvent`** (typed rows — read `.event` / `.ts` / `.userId`)
  instead of `List String` (JSON-object strings). Rendering code reads fields off
  the record now — e.g. `e.event ++ " · " ++ String.fromInt e.ts` — rather than
  treating each item as a JSON string.

---

## Low-level FFI proxies

These are thin wrappers around Go stdlib types — usually you'll reach for them only when interfacing with auto-generated FFI bindings.

### `Context`

Go's `context.Context`: `background`, `todo`, `withValue`, `withCancel`.

### `Fmt`

Go's `fmt`: `sprint`, `sprintf`, `sprintln`, `errorf`.

### `Ffi` — escape hatches

`call` (any Go func, dynamic), `callPure` (mark as pure), `callTask` (lift to Task), `has` (does symbol exist?), `isPure` (introspection).

> Reach for `Ffi.*` only when the auto-generated bindings can't model what you need. The built-in modules cover all common cases.

---

## Diverging functions

`System.exit : Int -> a` — process termination, polymorphic return so it works as the last expression in any case branch:

```elm
case validateConfig config of
    Ok ()  -> startServer config
    Err msg ->
        let
            _ = Log.error msg
        in
            System.exit 1
```

---

## Concurrency

```elm
import Sky.Core.Task as Task

-- Goroutine-backed parallel; first error short-circuits
allUsers =
    Task.parallel
        [ Db.getById db "users" 1
        , Db.getById db "users" 2
        , Db.getById db "users" 3
        ]
```

`Task.parallel : List (Task err a) -> Task err (List a)` —
concurrent task execution; the first error short-circuits the
batch.

`Task.lazy : (() -> a) -> Task err a` — defer a pure computation
so it can be sequenced with other tasks.

---

## The Prelude

`Sky.Core.Prelude exposing (..)` is implicitly imported everywhere. It re-exports:

`Result (Ok / Err)`, `Maybe (Just / Nothing)`, `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`.

You'll never need to write `import Sky.Core.Prelude` — it's already there.

---

## See also

- [Getting started](getting-started.md)
- [Language syntax](language/syntax.md)
- [Sky.Live overview](skylive/overview.md)
- [Sky.Auth overview](skyauth/overview.md)
- [Std.Db overview](skydb/overview.md)
- [Go FFI interop](ffi/go-interop.md)
- [Error system](errors/error-system.md)
- The dense AI-targeted reference lives in the project [`CLAUDE.md`](../CLAUDE.md#standard-library) — same surface, no narrative.
