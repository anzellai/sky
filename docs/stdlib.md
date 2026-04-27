# Standard library reference

Sky's standard library is **batteries-included** — there's one canonical module per concern, no plugin ecosystem to navigate, no `npm install` for crypto. This page is the complete user-facing reference.

> Each kernel module is reachable via its bare name. `import Log` works the same as `import Std.Log as Log`. The long `Sky.Core.X` / `Std.X` paths are kept for cross-language familiarity, but you can usually drop them.

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
| `modBy` | `Int -> Int -> Int` | Math modulo (Elm-style argument order) |
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

Highlights: `length`, `reverse`, `append`, `split`, `join`, `contains`, `startsWith`, `endsWith`, `toInt`, `fromInt`, `toFloat`, `fromFloat`, `toUpper`, `toLower`, `trim`, `replace`, `slice`, `repeat`, `padLeft`, `padRight`, `lines`, `words`, `htmlEscape`, `slugify`, `truncate`, `ellipsize`, `isEmail`, `isUrl`, `graphemes` (correct Unicode segmentation), `casefold`, `equalFold`.

### `List` — sequences

```elm
import Sky.Core.List as List

doubled = List.map (\n -> n * 2) [ 1, 2, 3 ]              -- [2, 4, 6]
sum     = List.foldl (\n acc -> n + acc) 0 [ 1, 2, 3 ]    -- 6
evens   = List.filter (\n -> modBy 2 n == 0) [ 1, 2, 3, 4 ] -- [2, 4]
```

`map`, `filter`, `foldl`, `foldr`, `length`, `head`, `tail`, `take`, `drop`, `append`, `concat`, `concatMap`, `reverse`, `sort`, `sortBy`, `member`, `any`, `all`, `range`, `zip`, `filterMap`, `parallelMap` (goroutine-backed), `isEmpty`, `indexedMap`, `find`, `cons`.

### `Dict` — key-value maps (string keys)

```elm
import Sky.Core.Dict as Dict

prefs = Dict.fromList [ ("theme", "dark"), ("lang", "en") ]
theme = Dict.get "theme" prefs   -- Just "dark"
```

`empty`, `insert`, `get`, `remove`, `member`, `keys`, `values`, `toList`, `fromList`, `map`, `foldl`, `union`.

> `Dict` keys are strings internally. If your keys are numeric, convert at the boundary: `Dict.insert (String.fromInt id) value`.

### `Set` — unique-element collections

`empty`, `insert`, `remove`, `member`, `union`, `diff`, `intersect`, `fromList`, `toList`, `size`.

### `Maybe` — optional values

```elm
import Sky.Core.Maybe as Maybe

name : String
name = Maybe.withDefault "Anonymous" maybeName
```

`withDefault`, `map`, `andThen`, `map2`, `map3`, `map4`, `map5`, `andMap`, `combine`, `traverse`.

### `Result` — fallible computations

```elm
import Sky.Core.Result as Result

case String.toInt input of
    Just n  -> compute n
    Nothing -> 0
```

`withDefault`, `map`, `andThen`, `mapError`, `map2`, `map3`, `map4`, `map5`, `andMap`, `combine`, `traverse`, `andThenTask` (bridge — see [Result/Task bridges](../CLAUDE.md#resulttask-bridges)).

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

`join`, `dir`, `base`, `ext`, `isAbsolute`, `safeJoin` (refuses `..` traversal).

### `Crypto` — hashes + entropy

```elm
import Sky.Core.Crypto as Crypto

digest = Crypto.sha256 "hello"   -- hex string
hmac   = Crypto.hmacSha256 "secret" "message"
```

| Function | Type | Notes |
|---|---|---|
| `Crypto.sha256` | `String -> String` | Hex digest |
| `Crypto.sha512` | `String -> String` | Hex digest |
| `Crypto.md5` | `String -> String` | Hex digest (legacy support only) |
| `Crypto.hmacSha256` | `String -> String -> String` | Hex digest |
| `Crypto.constantTimeEqual` | `String -> String -> Bool` | Side-channel safe comparison |
| `Crypto.randomBytes` | `Int -> Task Error Bytes` | OS entropy → raw bytes |
| `Crypto.randomToken` | `Int -> Task Error String` | OS entropy → hex string of given byte length |

### `Encoding` — base64, URL, hex

```elm
import Sky.Core.Encoding as Encoding

encoded = Encoding.base64Encode "hello"            -- "aGVsbG8="
decoded = Encoding.base64Decode encoded            -- Result Error String
urlSafe = Encoding.urlEncode "https://example.com/?q=hello world"
```

`base64Encode`, `base64Decode`, `urlEncode`, `urlDecode`, `hexEncode`, `hexDecode`. Encode functions return bare strings; decode functions return `Result Error String`.

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

| Decoder | Type |
|---|---|
| `Dec.decodeString` | `Decoder a -> String -> Result Error a` |
| `Dec.string`, `Dec.int`, `Dec.float`, `Dec.bool` | primitive decoders |
| `Dec.field` | `String -> Decoder a -> Decoder a` |
| `Dec.index` | `Int -> Decoder a -> Decoder a` |
| `Dec.list` | `Decoder a -> Decoder (List a)` |
| `Dec.map`, `Dec.map2`...`Dec.map5` | combine |
| `Dec.andThen` | dependent decoders |
| `Dec.succeed` / `Dec.fail` | constant decoders |
| `Dec.oneOf` | try decoders in order |
| `Dec.at` | `List String -> Decoder a -> Decoder a` (path traversal) |

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
| `Task.run` | `Task e a -> Result e a` | Force at the boundary |
| `Task.fromResult` | `Result e a -> Task e a` | Bridge from Result |
| `Task.andThenResult` | `(a -> Result e b) -> Task e a -> Task e b` | Chain Result step after Task |
| `Task.map2`...`Task.map5`, `Task.andMap` | combinators | |

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

| `Cmd.none` | `Cmd msg` | No-op |
| `Cmd.perform` | `Task err a -> (Result err a -> msg) -> Cmd msg` | Run task, dispatch result as Msg |
| `Cmd.batch` | `List (Cmd msg) -> Cmd msg` | Concurrent batch |
| `Sub.none` | `Sub msg` | No subscription |
| `Sub.every` | `Float -> (Posix -> msg) -> Sub msg` | Tick every N ms |

### `Time` — clock + duration

```elm
import Sky.Core.Time as Time

now =
    Time.now
        |> Task.andThen (\t -> println (Time.formatISO8601 t))
```

`now`, `sleep`, `every`, `unixMillis`, `formatISO8601`, `formatRFC3339`, `formatHTTP`, `format`, `parseISO8601`, `parse`, `addMillis`, `diffMillis`, `timeString`.

### `Random` — pseudo-random generation

```elm
import Sky.Core.Random as Random

dice = Random.int 1 6   -- Task Error Int
```

`int`, `float`, `choice` (pick from list), `shuffle`.

### `Http` — HTTP client

```elm
import Sky.Core.Http as Http

response =
    Http.get "https://api.example.com/users"
        |> Task.andThen (\body -> println body)
```

`get`, `post`, `request` (custom method/headers).

### `File` — filesystem

```elm
import Sky.Core.File as File

readme =
    File.readFile "README.md"
        |> Task.andThen (\content -> println content)
```

`readFile`, `readFileLimit`, `readFileBytes`, `writeFile`, `append`, `mkdirAll`, `readDir`, `exists`, `remove`, `isDir`, `tempFile`, `copy`, `rename`.

### `Io` — stdin / stdout / stderr

`readLine`, `readBytes`, `writeStdout`, `writeStderr`, `writeString`.

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

> `System.exit` has a polymorphic return so it works in any case branch — no need to make every other branch Task-shaped.

### `Process` — subprocess execution

```elm
import Sky.Core.Process as Process

result =
    Process.run "ls" [ "-la" ]
        |> Task.andThen (\output -> println output)
```

`Process.run` is the entire surface. (`exit`, `getEnv`, `getCwd`, `loadEnv` moved to `System` in v0.10.0.)

### `Db` / `Auth` / `Log`

These are big enough to deserve their own pages:

- **[Std.Db overview](skydb/overview.md)** — SQLite + Postgres, one API
- **[Std.Auth overview](skyauth/overview.md)** — bcrypt, JWT, register / login
- **[Std.Log](#stdlog)** — see below

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
| `Log.println` | `String -> Task Error ()` (alias for `Std.Log.println`) |
| `Log.debug`, `info`, `warn`, `error` | `String -> Task Error ()` |
| `Log.debugWith`, `infoWith`, `warnWith`, `errorWith` | `String -> List String -> Task Error ()` (key/value pairs) |
| `Log.with` | `List String -> Logger` (build a contextual logger) |

`SKY_LOG_FORMAT` (`plain` | `json`) and `SKY_LOG_LEVEL` (`debug` | `info` | `warn` | `error`) control output format and threshold. Configure defaults in `sky.toml` `[log] format = "json"`. See [Logging precedence](../CLAUDE.md#environment-variable-precedence).

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

Routing: `get`, `post`, `put`, `delete`, `any`, `static`, `group` (prefix), `use` (middleware).

Extractors: `param` (path), `queryParam`, `header`, `getCookie`, `formValue`, `body`, `path`, `method`.

Responses: `text`, `json`, `html`, `withStatus`, `redirect`, `cookie`, `withCookie`, `withHeader`.

### `Live` — Sky.Live (server-driven UI)

```elm
import Sky.Live as Live

main =
    Live.app
        { init = init
        , update = update
        , view = view
        , subscriptions = subscriptions
        , routes = [ Live.route "/" HomePage ]
        , notFound = HomePage
        }
```

See [Sky.Live overview](skylive/overview.md) for the full TEA flow.

### `Event` — Sky.Live event bindings

`onClick`, `onInput`, `onChange`, `onSubmit`, `onFocus`, `onBlur`, `onMouseOver`, `onMouseOut`, `onKeyDown`, `onKeyUp`, `onKeyPress`, `onImage` (with `fileMaxWidth` / `fileMaxHeight` / `fileMaxSize` constraints), `onFile`.

### `Html` — HTML elements

~70 element constructors: `text`, `div`, `span`, `p`, `h1`-`h6`, `a`, `button`, `input`, `form`, `table`, `tr`, `td`, ... plus `render` and `escape` helpers.

### `Attr` — HTML attributes

~60 attribute helpers: `class`, `id`, `style`, `type`, `value`, `href`, `src`, `checked`, `disabled`, `required`, ... plus `boolAttribute` and `dataAttribute` for custom attrs.

### `Css` — typed stylesheets

```elm
import Std.Css as Css

myStyles =
    Css.stylesheet
        [ Css.rule ".btn"
            [ Css.padding (Css.rem 0.5)
            , Css.background (Css.hex "#3b82f6")
            , Css.color (Css.hex "#ffffff")
            ]
        ]
```

~120 helpers covering layout (`flex*`, `grid*`), spacing (`px`, `rem`, `em`), colours (`hex`, `rgb`, `rgba`, `hsl`, `hsla`), typography (`font*`), transitions, transforms.

> Zero-arg CSS keywords (`Css.zero`, `Css.auto`, `Css.none`, `Css.transparent`) take `()` to sidestep zero-arity memoisation. Write `Css.margin (Css.zero ())`, not `Css.margin Css.zero` — see [Limitation #13](../CLAUDE.md#known-limitations-v09-dev).

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

Layout primitives: `el / row / column / paragraph / textColumn / text / none / button / input / form / link / image / html / layout`. Length: `px / fill / content / min / max`. Attributes: `padding / spacing / width / height / centerX / centerY / alignLeft / alignRight / pointer / style / htmlAttribute / name`. Events: `onClick / onSubmit / onInput (typed String→msg) / onChange / onFocus / onMouseOver / onMouseOut / onKeyDown / onFile / onImage`. File hints: `fileMaxSize / fileMaxWidth / fileMaxHeight`. Colour: `rgb / rgba / white / black / transparent`.

Sub-modules:
- **`Std.Ui.Background`** — `color`
- **`Std.Ui.Border`** — `color / width / rounded`
- **`Std.Ui.Font`** — `color / family / size / bold`
- **`Std.Ui.Region`** — `heading n / footer` (semantic landmarks for screen readers)
- **`Std.Ui.Input`** — typed form controls: `button / text / multiline / checkbox` + `labelAbove / labelBelow / labelLeft / labelRight / labelHidden / placeholder`
- **`Std.Ui.Lazy`** — `lazy / lazy2..lazy5` (no-op wrappers today; runtime memo deferred)
- **`Std.Ui.Keyed`** — `keyed` (emits `sky-key` for diff identity)
- **`Std.Ui.Responsive`** — `classifyDevice / adapt {phone, tablet, desktop}`

**Best-practice for forms with sensitive inputs (passwords, API keys):** wrap inputs in `Ui.form` and dispatch on `onSubmit DoSignIn` with a typed record. Do NOT wire `onInput` on the password field — that would dispatch the secret on every keystroke into Model and through every session-store write. See [Sky.Ui overview](skyui/overview.md#forms--the-password-best-practice-pattern) for the full pattern.

**File / image upload:** `Ui.onImage` auto-resizes to `fileMaxWidth × fileMaxHeight` (default 1200×1200) and re-encodes as JPEG @ 0.85 quality before sending; `Ui.onFile` ships the raw data URL. Both honour `fileMaxSize` for client-side caps. See [Sky.Ui overview](skyui/overview.md#file--image-upload).

Full reference, surface-coverage table, known limitations: [Sky.Ui overview](skyui/overview.md).

### `RateLimit` — request throttling

```elm
import Sky.Http.RateLimit as RateLimit

if RateLimit.allow "login" req then
    handleLogin req
else
    Task.succeed (Server.withStatus 429 (Server.text "too many attempts"))
```

`allow` (single bucket); use with `Middleware.withRateLimit` for declarative wiring.

### `Middleware` — composable handler wrappers

`withCors`, `withLogging`, `withBasicAuth`, `withRateLimit`. Each is a `Handler -> Handler` function — compose by chaining.

```elm
Server.use Middleware.withLogging
    (Server.use (Middleware.withRateLimit "api" 100)
        [ Server.get "/api/users" listUsers
        , ...
        ]
    )
```

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
import Sky.Core.List as List

-- Goroutine-backed parallel; first error short-circuits
allUsers =
    Task.parallel
        [ Db.getById db "users" 1
        , Db.getById db "users" 2
        , Db.getById db "users" 3
        ]

-- Pure parallel map
squared = List.parallelMap (\n -> n * n) [ 1..1000 ]
```

`Task.parallel : List (Task err a) -> Task err (List a)` — concurrent task execution, error short-circuits.

`Task.lazy : (() -> a) -> Task err a` — defer a pure computation to be run as a task.

`List.parallelMap : (a -> b) -> List a -> List b` — pure goroutine map.

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
