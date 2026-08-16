# Configuration architecture — build manifest, typed config, environment

> **Design of record.** The three-way split below is decided (recorded in
> `.claude/AUTONOMOUS_GOAL.md` at `564608e9`); this document designs it rather
> than evaluating it. Written against `feat/embedded-postgres` @ `517c3945`.
>
> **Revision note.** Two earlier drafts proposed a declarative schema generating
> readers for Rust, Go and the docs. That is now the rejected alternative (§10),
> kept with its reasons because the evidence in §1 still justifies why something
> had to change. Nothing verified was removed.

## Why this lives in `docs/tooling/`

The artefacts are tooling artefacts: a migration verb, a build-time hint, a gate,
and a shrunken manifest reference. It is **not** in `docs/rust-rewrite/`, which
is a numbered narrative of the compiler pipeline (00 → 14); this touches the
pipeline in two places (`lower.rs:795`, `lower.rs:2423`) and is otherwise
stdlib, runtime and CLI.

---

## The decision, in one page

| Layer | Owner | Written in | Holds |
|---|---|---|---|
| **Build manifest** | developer, at author time | `sky.toml` | what the compiler needs *before or during* compilation: project identity, entry, source root, output name, dependencies, and **compile-only flags such as `embed`** |
| **App config** | developer, in code | typed Sky — a `config` value built with `withX` | what the app *is*: session strategy, log format, database strategy, telemetry, ports |
| **Deployment** | operator, at run time | environment + `.env` | what varies per deployment: secrets, DSNs, per-replica overrides |

`embed` is the exemplar for layer 1 and worth stating plainly: `sky build --embed`
stages a PostgreSQL bundle and generates a `//go:embed` for it
(`build.rs:569-577`, `:1622-1630`). It changes *what is compiled into the
binary*. It cannot be a runtime value, and no amount of typed configuration can
make it one.

**An app that provides no config still runs.** Defaults reproduce today's
behaviour (§7); `withX` overrides them; the environment feeds them. The breaking
surface is only the `sky.toml` runtime keys that move — a much smaller set than
the config surface as a whole.

**The load-bearing argument: the compiler is the gate.** Every earlier draft
concluded that the gate mattered more than the schema, because the failure that
reached users was *configuration that looks set and does nothing* — a key in a
section nothing parsed, a key misspelled into inertness, a block parsed and read
by no one. Under this design those are not runtime failures. `Live.withStorePath`
either exists or the build fails; `Postgres` is a constructor, not a string that
might be `"postgress"`; a section that does not exist cannot be written. The type
checker closes the class that four years of hand-written parsers, mirrored
functions and adjacent-list discipline could not.

It closes *that* class, not every class. §9 keeps the one gate that is still
required, for the class typing cannot see.

---

## 1. The evidence

Every claim was read at the cited line on `517c3945`. **INFERRED** marks
reasoning over what was read rather than something read directly.

### 1.1 There is no TOML parser in the workspace

`rust/Cargo.toml`'s `[workspace.dependencies]` (lines 24–44) lists `la-arena`,
`smol_str`, `indexmap`, `annotate-snippets`, `rowan`, `logos`, `salsa`, `serde`,
`serde_json`, `include_dir`, `tower-lsp`. No `toml`, no `toml_edit`;
`rust/Cargo.lock` has no `toml*` package. `xtask/src/coverage_ledger.rs:1153`
states the policy: hand-parsed rather than adding a dependency.

### 1.2 Fourteen independent hand-rolled parsers

| # | Site | Section rule | Value rule |
|---|---|---|---|
| A1 | `project/src/build.rs:911` `read_sky_toml_config` | `starts_with('[')` + `find(']')` | `parse_toml_scalar` (`:841`) |
| A2 | `project/src/build.rs:1164` `sky_toml_project_key` | `starts_with && ends_with` | inline, `:1191-1196` |
| A3 | `project/src/build.rs:1221` `sky_toml_section_key` | `find(']')` | `parse_toml_scalar` |
| A4 | `sky/src/main.rs:660` `parse_toml_entry` | breaks at first `[` | own closure, `'` and `"` |
| A5 | `sky/src/main.rs:4472` `toml_entry` | **none** — greps anywhere | own closure |
| A6 | `sky/src/main.rs:2447` `db_driver_label` | `starts_with && ends_with` | `trim_matches('"')` |
| A7 | `sky/src/db_pool_sizing.rs:168` `toml_value` | `find(']')` | own `parse_toml_scalar`, `:191` |

Plus seven structural scanners: `ffi_ops.rs:1187` `read_dependencies`, `:1037`
`upsert_dependency`, `:1110` `remove_dependency`, `:1164` `is_sky_package_root`;
`db_provision.rs:166` `pinned_sky_toml`; `coverage_ledger.rs:1124`
`read_sky_toml`; `build_run_gate.rs:735` `declares_real_deps`.

Four different rules for recognising `[section]`; three for a scalar value.

### 1.3 Discipline was applied to keep two of them identical, and it failed

`db_pool_sizing.rs:189-190`, of its own `parse_toml_scalar`:

> Strip an inline comment and matching quotes — `project::build`'s
> `parse_toml_scalar`, kept behaviourally identical for the reason above.

It is not. On `maxOpenConns = "25"  # note`, `build.rs:841` returns `25`;
`db_pool_sizing.rs:191` sees the leading `"`, **skips** comment stripping, then
`trim_matches('"')` — which strips leading/trailing `"` characters, and the
string ends in `e` — returning `25"  # note`. They diverge the other way too:
`build.rs:841` does not handle `'`, so `'25'` is `'25'` there and `25` here.

The same class is live at `main.rs:2467` (`v.trim().trim_matches('"')`), whose
doc comment claims to mirror `read_sky_toml_config`; with
`driver = "postgres"  # prod` it returns `postgres"  # prod`, and its only
caller is the `sky db reset` / `drop` confirmation prompt (`main.rs:2538`).
**INFERRED** — read, not executed.

This is the load-bearing fact. Two adjacent functions, one written to mirror the
other, with a comment asserting they match, diverging on exactly the input the
original was fixed for. The remedy cannot be "be more careful".

The codebase reached the same conclusion independently. `517c3945` collapsed the
two production predicates into `func isProd() bool { return productionFromEnv() }`
(`rt.go:10219`):

> Keeping two predicates in agreement by convention had already failed once; the
> alias makes divergence impossible.

### 1.4 The same fact is written three times in one file

`build.rs:1073` `is_runtime_config_section` lists the sections; `build.rs:1082`
`accepted_config_keys` lists the keys, "kept adjacent so the two cannot drift
apart silently"; both restate the `match` arms at `build.rs:934-1075`.
`build.rs:1142` turns the third copy into a warning, and its doc comment records
the cost of its absence: `examples/08-notes-app` and `examples/12-skyvote` both
set `[auth] method`, `secret`, `session_ttl`, `email_verification` — none
parsed, three not keys at all, and `session_ttl` is `tokenTtl` misspelled, so
both examples advertised a 24-hour session and got the default.

The three `withX` kernels have the same problem one layer down: `liveCfgSet`
(`live_config.go:56`), `tuiCfgSet` (`tui_config.go:29`) and `cliCfgSet`
(`cli_config.go:22`) are three byte-identical copies, and the "FOUR INVARIANTS"
comment is restated in near-identical prose in all three files
(`live_config.go:16-32`, `tui_config.go:1-14`, `cli_config.go:1-8`).

### 1.5 The tree has already stated the governing principle

`2fab8d99` wired `[security] csrf` (`build.rs:1070`, accepted at `:1155`) and
added `inert_key_hint` (`build.rs:1166`), which **refuses** `[security] env`:

> Set the `ENV` environment variable on the deployment instead
> (`ENV=production`) … Which environment a binary runs in is a property of the
> deployment, not of the build, so it is not a sky.toml key.

That sentence is a rule about ownership, and the decided design is that rule
applied to the whole surface.

### 1.6 Two env namespaces, and the cost

`env_prefix.go` defines the internal namespace: `skyEnvName(suffix)` (`:80`) =
`envPrefix + "_" + suffix`; `skyGetenv` (`:94`) / `skyLookupEnv` (`:86`) read
through it; `SetEnvPrefix` (`:50`) comes from `[env] prefix`; `SetSkyDefault`
(`:107`) seeds under the same prefix. There is no fallback inside `skyGetenv`.
Only two names carry a second spelling: `LIVE_STATIC_DIR` → `STATIC_DIR`
(`live.go:3976/3983`) and `DB_PATH` → `DATABASE_URL` (`db_auth.go:242-247`).

`517c3945` fixed the production predicate to route its fallback through
`skyGetenv` (`observability.go:347-360`). Three settings are still read through
**both** namespaces, so under a custom prefix each is two variables gating one
thing:

| Setting | Prefix-aware | Hardcoded |
|---|---|---|
| console admin token | `skyGetenv("ADMIN_TOKEN")` `console.go:403` | `os.Getenv("SKY_ADMIN_TOKEN")` `console_auth.go:74` |
| Live base path | `skyGetenv("LIVE_BASE_PATH")` `live.go:3966` | `os.Getenv("SKY_LIVE_BASE_PATH")` `console.go:142,287` |
| synchronous commit | `ANALYTICS_SYNCHRONOUS_COMMIT` `analytics_writer.go:227` | `SKY_TELEMETRY_SYNCHRONOUS_COMMIT` `telemetry/persist.go:842` |

`analytics_writer.go:230-232` asserts the last pair "cannot drift in meaning".
They already drift in namespace.

### 1.7 One name, three readers, three defaults

`LIVE_TTL` is read at three sites, spelling **two** distinct defaults three
different ways:

- `live.go:4013` — `parseTTL(skyGetenv("LIVE_TTL"), stringField(cfg, "Ttl"), 30*time.Minute)`
- `csrf_middleware.go:82` — `parseTTL(skyGetenv("LIVE_TTL"), "", 30*24*time.Hour)`
- `subapp_inprocess.go:400` — `parseTTL(skyGetenv("LIVE_TTL"), stringField(cfg, "Ttl"), defaultSubAppSessionTTL())`

> **Corrected 2026-08-16.** An earlier draft of this section said "three
> different defaults". `defaultSubAppSessionTTL()` returns `30 * time.Minute`
> (`subapp_inprocess.go:423-425`) — the same value as `live.go:4013`, written
> differently. The argument is unaffected and arguably sharper: **two** sites
> agree on 30 minutes and spell it two ways, so a change to one is invisible to
> the other, while the third reads the same variable as 30 days.

`csrf_middleware.go:68-69` records the scar: keying the CSRF cookie's Max-Age to
`SKY_LIVE_TTL` broke it, because `SKY_LIVE_TTL=30m` is "the documented production
pattern" for sessions. One variable, a 30-minute session lifetime and a 30-day
cookie window, and an operator who sets it gets both.

### 1.8 Precedence is already inconsistent *within one module* — and one builder is dead

This is the sharpest single piece of evidence for §3, and it is new.

`Std.Live` exposes five builders that overlap a `sky.toml` key. They resolve in
**three different orders**:

| Builder | Order | Evidence |
|---|---|---|
| `withPort` | operator env → `withPort` → `sky.toml` → `8080` | `live.go:3859-3873`, using `isSeededDefault` to tell an operator's env from a seeded one |
| `withStore`, `withStorePath`, `withStatic` | **config first**, env only when the field is empty | `selectStore` `live_store.go:1753-1759`: `if kind == "" { kind = skyGetenv("LIVE_STORE") }` |
| `withTtl`, `withIdleEvict` | **env first**, config second | `parseTTL` `live_store.go:307-325` iterates `[]string{envVal, tomlVal}` |

And two of the three are documented backwards:

- `sky-stdlib/Std/Live.sky:167-168` says of `withStore`: "Env `SKY_LIVE_STORE`
  wins." The code says the opposite.
- `live.go:3992-3995`, the comment *directly above the call*, says "env vars
  `<PREFIX>_LIVE_STORE` / `<PREFIX>_LIVE_STORE_PATH` take precedence over
  config". The code four lines below says the opposite.

**`Live.withTtl` is dead code.** `lower.rs:822` emits
`rt.SetSkyDefault("LIVE_TTL", "1800")` **unconditionally, for every program**.
`SetSkyDefault` is set-if-unset, so `<PREFIX>_LIVE_TTL` is *always* set by the
time `liveAppRun` runs. `parseTTL` takes the first parseable value and the
environment is argument one — so the `withTtl` value in argument two can never
be reached. This is precisely the defect `withPort` had before the
`isSeededDefault` fix (`live.go:3826-3874`), still present in the same module.
**VERIFIED** by reading `lower.rs:822`, `live.go:4013`, `live_store.go:307-325`;
not executed.

Hidden precedence produced all of this. §3 removes the hiding.

### 1.9 Three files hand-register a repair for one ordering problem

`SetSkyDefault` (`env_prefix.go:107-119`) re-runs `envPrefixHooks` because
package-level env-derived variables were evaluated before the generated `init()`
ran. Three files register such a hook: `rt.go:1214-1218` (`logThreshold`,
`logJSON`), `live.go:7155` (`loadSseChanBuffer`), `csrf_middleware.go:110`
(`refreshCsrfEnabled`), whose comment names the hazard: "the generated `init()`
seeds the sky.toml default AFTER this package's `init()` has already run, so
without this hook `[security] csrf = false` would be written to the env too late
to be seen." Every new setting captured at package init must remember this.
Forgetting is silent.

**And one has already forgotten.** `streamDebug`
(`http_stream.go:319`) is a package-level `var` — so it evaluates before
*every* `init()`, including `dotenv.go`'s at `:106` — and it has no hook. Three
hooks are registered (`csrf_middleware.go:110`, `live.go:7155`,
`rt.go:1215`) against four env-reading captures that could take one.
`SKY_STREAM_DEBUG=1` in a `.env` file therefore does nothing, permanently, and
nothing says so; only an `export` before the process starts works. It reads a
hardcoded name rather than a prefixed one, so `onEnvPrefixChange` would not
have rescued it either — the ordering, not the namespace, is the defect. The
same package-level-`var` shape carries `logThreshold`/`logJSON`
(`rt.go:1207-1208`), which ARE rescued, which is what makes the omission easy
to miss: the file next door does it correctly.

The remaining four `rt` `init()`s that read the environment apply their values
**irreversibly** and could not be repaired by a hook even if one were
registered: `gc_tuning.go:314` (`debug.SetMemoryLimit` / `SetGCPercent`),
`observability.go:58` (opens the telemetry DB and starts its flusher),
`profile.go:71` (`pprof.StartCPUProfile`, `signal.Notify`) and `dotenv.go:106`
(mutates the process environment). Those are §4.3's R5 and are correctly there.

### 1.10 Provenance exists, for one consumer

`SetEnvDefault` records what it seeded in `seededDefaults` (`dotenv.go:47-50`,
marked at `:64`); `setEnvRaw` (`:96`) clears the mark, so a `.env` value counts
as operator-set and a `sky.toml` value does not. One non-test reader:
`resolveLivePort` via `isSeededDefault` (`live.go:3859`).

### 1.11 The `[auth]` block is write-only, and the tree knows

Parsed at `build.rs:1045-1047`, accepted at `:1106`, emitted at `lower.rs:819`,
defaulted at `:823-825`. Read by nothing: `AUTH_` across `runtime-go/` hits only
comments (`env_prefix.go:5,16`, `startup_report.go:70`); no
`skyGetenv`/`skyLookupEnv` takes an `AUTH_*` suffix.
`startup_report_test.go:110-111` asserts the startup banner must **not** name
`SKY_AUTH_TOKEN_SECRET`, "which no runtime code reads" — a test protecting the
knowledge that these are not runtime settings, while `docs/sky-toml.md:182-186`
still tabulates them as configuration with effect.

### 1.12 The reconciliation, measured

Over live `docs/**` (excluding `docs/history/`) against non-test readers, with
the reader set taken as literal `SKY_*` **unioned with the suffix form**:

- **25 documented names have no reader.** Six appear nowhere in the repo
  (`SKY_LSP_DEBUG`, `SKY_LSP_TRACE`, `SKY_SUBAPP_VERBOSE`, `SKY_ANALYTICS_DEBUG`,
  `SKY_LIVE_MAX_SUBS_PER_SESSION`, `SKY_ADT`); three are `[auth]`; three are
  self-declared dead; five are read only by the retired Haskell compiler
  (including `SKY_SOLVER_BUDGET`, still described as live at
  `docs/sky-toml.md:559-560` and `env_prefix.go:24-25`); eight only by scripts.
- **46 read names appear in no live doc**: 18 console-hub/spool names, 5 HTTP
  timeouts, 13 toolchain names.
- **39 suffixes are read** through the prefixed namespace; **42** are seeded. The
  gap is `[auth]`.

An earlier audit reported 31 and 48 and claimed the DB pool knobs were
undocumented. **That claim is false** — documented at `docs/sky-toml.md:261-266`,
read at `db_pool.go:514-517`. The audit was misled because the docs only ever
write `<PREFIX>_DB_MAX_OPEN_CONNS` while the runtime warns with the resolved
literal via `skyEnvName(…)` (`db_pool.go:525`). **The name an operator reads in a
warning is not findable in the documentation** — a discoverability defect that
defeated an audit of discoverability.

### 1.13 Silence is the dominant failure mode

- **Session store.** `live_store.go:1823-1825`: `case "", "memory":` logs one
  neutral line. Unset and *explicitly chosen* `memory` take the same branch, the
  text is identical in production, and it says nothing about "lost on restart" —
  a phrase that exists twenty lines away (`:1851`) for the *fallback* case. The
  startup report does not mention the session store; `sky doctor` does not check
  it (`main.rs:3427-3435`).
- **Jobs store.** `jobs_kernel.go:312-318`: unset defaults to `memory` with **no
  log line at all**.
- **Analytics.** `analytics_store.go:211-229` swallows open failures with a bare
  `return` at three points; the only surface is a once-per-process warn that
  fires when an event is emitted (`:346-353`) — an hour later, or never. An
  unparseable `retention` (`"90days"`, `"3 months"`) returns 0 at `:286-302`,
  silently meaning "keep everything forever".
- **Driver detection.** `detectDriver` (`db_auth.go:376-389`) has a bare
  `default: return "sqlite"`, so `mysql://…` becomes a SQLite file with that
  literal name.

The counter-example is `db_pool.go`, the best-behaved surface in the tree: it
warns when a knob is inert on SQLite (`:502-512`), when `MaxOpenConns=0` means
unlimited (`:521-525`), when an isolation value is unrecognised (`:576-587`),
and when a value will not parse (`:715-745`). The difference is not the
mechanism — someone wrote the warnings.

### 1.14 Errors name keys, and the names rot

- `observability.go:228` served `hint: "set [security] env …"` while
  `[security]` was unparsed. `2fab8d99` closed the silence with `inert_key_hint`,
  which is the right fix and also proves the class.
- `console_auth_v2.go:448` returns `hint: "set SKY_METRICS_TOKEN …"`.
  `SKY_METRICS_TOKEN` is the **middle** of a three-name chain —
  `SKY_ADMIN_TOKEN`, `SKY_METRICS_TOKEN`, `SKY_CONSOLE_TOKEN_SECRET`
  (`console_auth.go:74-80`) — and the startup banner recommends the *first*,
  with a comment (`startup_report.go:63-74`) calling `SKY_METRICS_TOKEN` "its
  back-compat alias". The 401 tells the operator to set a name the banner calls
  legacy.
- `telemetry/otel.go:57-59` and `observability.go:255-259` still cite
  `[observability] service_name` / `[observability] enabled`, which no parser
  has ever had.

### 1.15 The manifest the scaffold writes is the legibility complaint

`main.rs:1220-1235` writes bare top-level `name`/`version`/`entry`/`bin` while
`docs/sky-toml.md:48-69` documents them under `[project]`; a whole `[source]`
section for one key; `port` readable both bare and as `[live] port`
(`build.rs:935`, `:1016`); a decorative `[database] driver` that
`build.rs:936-938` says is "RECORDED, never emitted"; and a production path
taught by **commented-out duplicate section headers** (`# [live]`,
`# [database]`) that would be invalid TOML if uncommented as written. It also
suggests `cookieName = "sky_sid"`, colliding the auth cookie with Sky.Live's
session cookie.

The four places a user configures "where state lives" are `[database] path`,
`[live] store`, `[jobs] store`, `[analytics] dbPath` — one decision, four
sections — while `main.rs:1205` and `:1295` both tell the user the production
shape is *one* URL wiring all four together.

### 1.16 `docs/sky-toml.md` has drifted from itself

The "at a glance" table (`:30-39`) omits `[jobs]` (documented at `:466`),
`[analytics]` (a parsed section with no section in the file), `[security]` and
`[source]`. `[database]` (`:210-462`) is five topics, two documenting no
`[database]` key at all — including "Garbage collection" (`:439`), which opens
"There is no sky.toml knob for the collector". `:203` says keys are camelCase;
`:482` accepts `store_path` because a runtime error message used it.
`docs/sky-toml.md:177` gives `tokenTtl = 86400` while
`docs/skyauth/overview.md:198` gives `tokenTtl = "24h"`.
`docs/skylive/pubsub-design.md:1109` documents a `[live.broker]` section no
parser has.

---

## 2. What already exists

The design is not a new mechanism. It is a half-built one, and knowing exactly
how far it is built determines how much work this is.

**`Std.Live`** — opaque config, `config` constructor, **fourteen** builders
(`sky-stdlib/Std/Live.sky:51` `type AppConfig model msg = AppConfig_OPAQUE`,
constructor not exposed at `:22-41`; `config` at `:67`; `app` at `:79`):
`withHead :107`, `withConsoleAuth :114`, `withOnNavigate :126`, `withGuard :133`,
`withStatic :140`, `withStaticUrl :146`, `withPort :163`, `withStore :170`,
`withStorePath :176`, `withTtl :182`, `withIdleEvict :193`, `withAnalytics :200`,
`withAnalyticsIdentify :212`, `withStatus :219`.

**`Std.Tui`** — same shape: opaque at `:39`, `config` at `:46`, two entry points
(`app :56`, `program :63`), four builders (`withOnKey :71`, `withGuard :78`,
`withCanvasWidth :84`, `withCanvasHeight :90`).

**`Std.Cli`** — same shape: opaque at `:27`, `config` at `:33`, `program` at
`:43`, one builder (`withOnLine :49`).

**`Std.Webview`** — the odd one out. Plain exposed `type alias` records
(`WindowCfg :30`, `AppCfg :66`) with pure-Sky record-update builders
(`withTitle :45` = `{ cfg | title = title }`, `withSize :51`), and a docstring
(`:80-88`) showing a record *literal* as idiomatic.

**`Sky.Http.Server`** — no config value at all:
`listen : Int -> List Route -> Task Error ()` (`Sky/Http/Server.sky:147`).

**`Std.Jobs`** — no configuration surface:
`module Std.Jobs exposing (Job, JobId, define, enqueue, enqueueIn, cancel)`. The
backend is chosen in `chooseJobsStore` (`jobs_kernel.go:294-350`) from
`skyGetenv("JOBS_STORE")`.

**`Std.Db`** — `connect : () -> Task Error Db` (`Std/Db.sky:77`), taking unit and
reading the environment; `open : String -> String -> Task Error Db` (`:70`).

**`Std.Log`** — no configuration surface; format and level are env-only
(`rt.go:1205-1207`).

**How a builder value reaches Go.** The record passed to `config { … }` is
lowered to a nominal Go struct (`lower.rs:275-277,1426,1474,1540-1591`);
`Live_config` (`live_config.go:37-48`) reads it reflectively and materialises a
`map[string]any` keyed by PascalCase names; each `withX` shallow-clones the map
and sets one key (`liveCfgSet`, `:56-69`); `liveAppRun` reads with
`rt.Field(cfg, "…")`, which handles both structs and maps
(`rt.go:5922-5973`). Two invariants matter downstream: **an unset optional is
absent from the map, never a typed nil** (`live_config.go:20-24`), and callbacks
are stored verbatim, never asserted to a Go func type (`:25-27`).

**There is precedent for exactly this migration.**
`docs/v0.19/migration-builder-cfg.md` records the v0.19 move from record literals
to builders, including the full old-field → `withX` map (`:53-66`). This design
extends a migration the project has already performed once.

**So the gap is precisely this**: per-shape config exists and is nearly uniform;
**every cross-cutting concern is outside it** — database, jobs, log, analytics,
telemetry, console, CSRF. That is the gap `sky.toml` was filling, badly.

---

## 3. Precedence: the environment is read explicitly, in Sky

Two coherent designs exist. §1.8 decides between them.

**Implicit** — the runtime resolves `default → env → withX` (or some other
order) behind the scenes, as today. **Explicit** — the environment read happens
*inside* the config expression, written by the user:

```elm
config =
    Sky.Config.default
        |> withSessionStore (Env.getOr "SESSION_STORE" Postgres)
```

**Explicit wins, and §1.8 is the argument.** One module, five builders, three
different precedence orders; two of them documented backwards in two places
each; and one builder — `withTtl` — that cannot win under any input because the
compiler unconditionally seeds the variable it loses to. Nobody decided that.
It accumulated, because the ordering lives in Go, spread across `resolveLivePort`,
`selectStore` and `parseTTL`, and nothing forces those three to agree.

Under the explicit form the ordering **is the expression the user wrote**, in
their own `main`, in the language they are already reading. There is no order to
document, so there is no order to document wrongly. `withTtl` could not be dead,
because there is no second path for it to lose to.

Four further consequences, all good:

1. **Fallback chains become ordinary values.** `DB_PATH → DATABASE_URL`
   (`db_auth.go:242-247`) and `LIVE_STATIC_DIR → STATIC_DIR`
   (`live.go:3976/3983`) are hardcoded in Go today. They become
   `Env.getOr "SKY_DB_PATH" (Env.getOr "DATABASE_URL" (Sqlite "app.db"))`,
   visible and editable.
2. **`[env] prefix` largely evaporates.** Its purpose was to namespace names the
   runtime chose. When the user writes the literal name, they namespace it
   themselves. The prefix survives only for the residual surface (§4).
3. **A missing required value fails typed and loud.** `Env.require "DATABASE_URL"`
   is `Task Error String`; absence is a reported startup failure, not an empty
   string flowing into `detectDriver`'s silent SQLite fallback (§1.13).
4. **The app's environment contract is derivable from its source.** A static pass
   over the `config` expression can extract every literal `Env.*` name, which is
   what `sky config env` prints. **INFERRED** — this needs a small extraction
   pass, and only *literal* names are extractable; a computed name is invisible
   to it and must be reported as such rather than silently omitted.

### 3.1 What the loser costs

Two real costs, stated plainly.

**Only wrapped settings are overridable.** Under 12-factor-style implicit
resolution, *any* setting can be overridden without a code change. Under the
explicit form, if the developer did not wrap it in `Env.getOr`, an operator
cannot override it. That is a genuine loss of flexibility, and the honest
defence is that the flexibility is what produced §1.12: 46 names read in no
document, every value silently overridable by a spelling nobody wrote down.
Explicit makes the set of deployment knobs finite, visible in `main`, and
printable. An operator who needs one that is not wrapped has to ask for it —
which is a conversation, not a silent failure.

**More typing for the common case.** Mitigated by strategy-level helpers, so the
common shape is one line rather than one line per knob:

```elm
|> withDatabase (Db.fromEnvOr "DATABASE_URL" (Sqlite "app.db"))
|> withSessions Sessions.sharedWithDatabase
```

### 3.2 `config` is an effectful value

Reading the environment is an effect, and Sky's rule is that effects are `Task`.
So the entry point is:

```elm
config : Task Error Config
```

The generated preamble runs it before anything else (§4.2). This is not a
concession — it is what makes `Env.require` able to fail properly, and it keeps
the language's central rule intact. It also avoids the CAF trap: a zero-argument
top-level binding is memoised (AGENTS.md), which is correct for config and would
have been a silent hazard if config could contain a fresh-value read.

A project that writes `config : Config` (pure, no `Env` calls) is accepted too —
the compiler lifts it. Most tier-1 apps will.

### 3.3 `.env` loading stays automatic, and gains an explicit form

`.env` is loaded today at `rt` package init from the process working directory
(`dotenv.go:106`), never overriding an already-set variable (`:162`). **That
stays**, for two reasons: removing it would break every existing app, violating
the "existing apps still run" rule; and a `.env` file is a thing that *fills the
environment*, not a config source in its own right — once loaded, `Env.get` sees
it, so automatic loading is orthogonal to explicit reads.

Added: `Env.loadFile : String -> Task Error ()` for the "provided .env file"
case, runnable at the top of the config Task:

```elm
config =
    Env.loadFile "deploy/staging.env"
        |> Task.andThen (\_ -> Task.succeed (Sky.Config.default |> …))
```

Five `.env` parser facts constrain any tooling that touches the file, all read in
`dotenv.go`:

1. **Duplicate keys: the FIRST wins** (`:162`, because the second occurrence
   finds the variable already set). The opposite of most `.env` tooling.
2. **`export FOO=bar` is not stripped** — the key becomes the literal
   `export FOO`, which does nothing.
3. **Comments are not modelled** by the loader, so a round-trip through it
   destroys them.
4. **Quoting and inline comments have specific rules** (`stripDotEnvValue:187`):
   a quoted value ends at the first matching quote and the rest is discarded; an
   unquoted `#` starts a comment only when preceded by a space or tab, so
   `URL=https://x/#frag` is safe and `URL=https://x/ #frag` is truncated.
5. **A second, divergent `.env` parser exists** — `db_pool_sizing.rs:203-224`,
   whose comment claims to match `rt`'s loader and which has no inline-comment
   handling at all. `SKY_DB_MAX_OPEN_CONNS=55 # tuned` is `55` to Go and
   `55 # tuned` to Rust. §1.3 again, in the other file format, and it is live.

### 3.4 What a deployment sets for a value the app never reads from env

Nothing. It cannot. This is the direct consequence of §3.1 and must be said
rather than papered over. Three mitigations, in order of preference:

1. `sky config env` prints exactly what this app reads, so the answer is
   discoverable rather than guessable.
2. The stdlib's strategy helpers wrap the settings an operator realistically
   needs (DSN, session store, log level, telemetry endpoint, ports), so the
   default scaffold is already overridable in the ways deployments actually
   require.
3. The residual runtime surface (§4.3) remains env-driven for the settings that
   genuinely cannot move.

---

## 4. Bootstrap ordering, and the residual surface

Go's initialisation order is fixed: imported package (`rt`) variable
initialisers and `init()`s → `main`'s package-level variables → `main`'s
`init()` (the generated prologue) → `main()`.

### 4.1 What runs before user code could exist

**`rt` package init** — fourteen `init()` functions (`decimal_kernel.go:67`,
`dotenv.go:106`, `csrf_middleware.go:103`, `gc_tuning.go:314`,
`live_redis_broker.go:347`, `live_store.go:161`, `live.go:286`, `live.go:7153`,
`observability.go:58`, `money_kernel.go:165`, `rt.go:1214`, `profile.go:71`,
`time_zones.go:58`). Five touch configuration:

| Site | Reads | Applied |
|---|---|---|
| `gc_tuning.go:314-316` | ambient `GOMEMLIMIT`/`GOGC` + detected RAM | **immediately** — `debug.SetMemoryLimit` (`:286`), `debug.SetGCPercent` (`:289`) |
| `dotenv.go:106` | `.env` from the process CWD | into the environment |
| `csrf_middleware.go:103` | `<PREFIX>_CSRF` | `atomic.Bool`, repaired by a hook |
| `rt.go:1205-1207` | `LOG_LEVEL`, `LOG_FORMAT` | package vars, repaired by a hook |
| `observability.go:58` | `SKY_CONSOLE_DB_PATH` | telemetry persistence |

**The generated prologue `init()`** — `lower.rs:795-827`: `SetEnvPrefix` (`:811`),
`SetPortDefault` (`:814`), one `SetSkyDefault` per `sky.toml` key (`:819-821`),
four hardcoded fallbacks (`:822-825`).

**The generated `main` preamble** — `lower.rs:2423-2440`, four fixed statements
in a documented order: `defer rt.LogPanicAndExit()`,
`rt.MaybeStartEmbeddedPostgres()`, `defer rt.StopEmbeddedPostgres()`,
`rt.MaybeApplyEmbeddedMigrationsAndExit()`. The doc comment (`:2386-2412`)
records that the PostgreSQL start *must* be in `main`, not an `init()`, so it can
see the prologue's `SetSkyDefault("DB_PATH", …)`.

### 4.2 Where the config value applies

The compiler looks for `Main.config` exactly as it looks for `Main.main`, and
emits its application as the **first** statement of the preamble:

```go
func main() {
    defer rt.LogPanicAndExit()
    rt.ApplyConfig(Main_config())        // ← runs the config Task, then applies
    rt.MaybeStartEmbeddedPostgres()
    defer rt.StopEmbeddedPostgres()
    rt.MaybeApplyEmbeddedMigrationsAndExit()
    …
}
```

`ApplyConfig` uses the **existing** `SetEnvDefault` set-if-unset semantics
(`dotenv.go:59`), so **no runtime read site changes**. The runtime keeps one
resolution mechanism, already implemented and already tested; the config value
supplies the layer `sky.toml` used to supply. That is what makes this
incremental rather than a rewrite, and it is why `MaybeStartEmbeddedPostgres`'s
ambiguity check keeps working — it now sees the code-declared DSN too.

A project with no `config` binding gets `Sky.Config.default`, which is what every
project effectively gets today.

Two scars close on their own. The `envPrefixHooks` mechanism (§1.9) becomes
unnecessary for anything read through the config value. And `lower.rs:815-818`'s
careful ordering — sky.toml defaults before the hardcoded fallbacks, with a
comment recording that the other order "silently clobbered `[live] ttl` /
`[auth] *`" — folds away, because there is one source.

### 4.3 The residual runtime surface — exactly what cannot move

This is the deliverable, and it is small.

**R1. Compile inputs.** `project.name`, `entry`, `root`, `bin`, `[deps]`,
`[deps.go]`. The compiler needs them to know what to compile.

**R2. Compile-only flags.** `embed` — `sky build --embed` stages a bundle and
generates a `//go:embed` (`build.rs:569-577`, `:1622-1630`); `postgresVersion`
pins which bundle (`db_provision.rs:154`). These change the artefact.

**R3. Toolchain inputs consumed with no binary.** `sky run` reads
`[database] embedded` to decide whether to supervise a cluster (`build.rs:989`);
`sky db migrate` runs with the app absent. Read by CLI verbs *before* a binary
exists.

**R4. `[env] prefix`.** It shapes the *names* of R5, so it must be a
compile-time constant for the generated code, the CLI and the docs to agree.
Under §3 its scope shrinks to the residual surface only.

**R5. Applied at `rt` package init, before any user code.**
`GOMEMLIMIT`/`GOGC` (`gc_tuning.go:314-316`) — and it is a property of the
machine, not the app, so the environment is its right home anyway.

**R6. Deployment identity.** `ENV` / `<PREFIX>_ENV`, already settled by
`build.rs:1168-1173`. Secrets. The DSN.

That is the whole residue: what the compiler needs, what the machine supplies,
and what the operator owns. Nothing in between — which is the validation the
design needed.

### 4.3.1 What the measurement added to R1–R6

R1–R6 above were reasoned out. `xtask config-surface` then counted them from the
sources (`docs/coverage/config-surface.json`, regenerated by the gate; every
number in this subsection comes from that file, none from a hand count). It
found **14 pre-binary surfaces** where R1–R6 predicted six classes, and the
difference is four `[database]` keys plus two readers the R-classes do not
describe:

| Surface | Read by | Why R1–R6 missed it |
|---|---|---|
| `[database] path`, `[database] url` | `check_run_config` (`db_cluster.rs:1658,1662`) on `sky run` / `sky watch` | R6 calls the DSN the operator's, and it is — but the **ambiguity refusal** (`--embed` alongside an explicit DSN is an error, not a precedence rule) has to read both *before* the build to refuse |
| `[database] maxOpenConns` | `resolve_max_open_conns` (`db_pool_sizing.rs:143`) on `sky db start` | §4.4 already names this casualty and answers it with `./sky-out/app --sky-config`. That answer needs a binary, and `sky db start` in a fresh tree has none |
| `[database] driver` | `db_driver_label` (`main.rs:2447`) on `sky db reset` / `drop` | read before the build, for the confirmation prompt — by a *third* hand-rolled scalar parser whose doc comment claims to mirror `read_sky_toml_config` and does not (§1.3) |
| `[live]` / `[auth]` section **headers** | `check_auth_secret` (`main.rs:3678`) on `sky doctor` | not a key at all: an inline test for a header's presence, so no key-based reader can see it |

**What this costs the design.** Not the split — the additions are settings the
manifest can carry, not a new layer, so §10's schema stays rejected (risk 1).
It costs three specific things:

1. **§6's manifest is one section short.** `[database] path`/`url` cannot move
   to "environment, with a code fallback" *and* keep the ambiguity refusal
   working before a build. Either the refusal moves into the built binary
   (where `MaybeStartEmbeddedPostgres` already lives) or the manifest keeps a
   DSN-shaped key it is not supposed to have. That is a decision §6.1 does not
   currently record.
2. **§4.4's answer is narrower than it reads.** `--sky-config` covers
   `provision --shared`; it does not cover `sky db start`, and §4.4 says so in
   passing. The measurement makes it a counted surface rather than an aside.
3. **Two hand-rolled parsers survive the §6.2 cull.** `db_driver_label` and
   `check_auth_secret` read `sky.toml` with their own scanners and no key
   argument. §6.2 retires `db_driver_label` with `[database] driver`; nothing
   in §6.2 retires `check_auth_secret`.

Three further counts come from the same scan and are ratcheted alongside:
**3** env suffixes the compiler seeds into every program that nothing under
`runtime-go/` reads (the whole `[auth]` block — §1.11, now mechanical), **10**
documented `SKY_*` names used nowhere else in the tree, and **29** read names in
no live doc. Those last two are **not comparable to §1.12's 25 and 46**: the
generated counts take "used" as the whole tracked tree outside `docs/`, unioned
with the suffix form, where §1.12 counted against non-test readers and
classified the residue by hand. Both are real; only one is regenerated on every
run, and per AGENTS.md it is the one later documents may quote.

**Two settings that look like R5 and are not.** `SKY_CSRF`
(`csrf_middleware.go:103`) and `LOG_LEVEL`/`LOG_FORMAT` (`rt.go:1205-1207`) are
*captured* at package init but *repaired* by hooks, so they already tolerate a
later value and can move into config. And `Std.Jobs` is safe: `jobsBoot`
(`jobs_kernel.go:86`) is lazy, guarded by `jobsRuntimeStarted` and called from
`enqueue`/`define` (`:206`, `:233`, `:261`), so it runs after `main` has applied
the config. **VERIFIED** by reading the boot guard and its callers.

### 4.4 One casualty worth naming

`sky db provision --shared` sizes a cluster by reading `[database] maxOpenConns`
from `sky.toml` (`db_pool_sizing.rs:126-143`). If the knob moves into code, a
Rust CLI cannot read it without running the program.

The answer is `./sky-out/app --sky-config`, which prints the applied
configuration as JSON. This is strictly *more* accurate than a static read — it
reflects what the binary will actually do, including `.env` and defaults — and
it generalises to `sky config --from ./sky-out/app`, the only honest answer to
"what is my deployed binary configured with". It needs a build to exist, which
is fine for `provision --shared` and not for `sky db start` in a fresh tree;
there, the pool knobs remain deployment-layer, which they arguably always were
(sizing depends on the server's `max_connections`, §11).

---

## 5. App shapes

### 5.1 Cross-cutting config is a separate value from app-shape config

`Std.Config` is **taken** — it is a TOML/YAML/JSON decoder library for the
user's own files (`sky-stdlib/Std/Config.sky:1-47`). The new module is
`Sky.Config`, which is free (`sky-stdlib/Sky/` holds only `Core`, `Http`,
`Test.sky`) and correctly namespaced: `Sky.*` is the framework's own.

```elm
type Config                                        -- opaque, AppConfig-style
default        : Config
withDatabase   : Database -> Config -> Config
withSessions   : Sessions -> Config -> Config
withJobs       : JobStore -> Config -> Config
withLog        : LogFormat -> LogLevel -> Config -> Config
withTelemetry  : Telemetry -> Config -> Config
withConsole    : ConsoleAuth -> Config -> Config
withCsrf       : Bool -> Config -> Config

type Database  = Sqlite String | Postgres String
type Sessions  = Memory | SessionsSqlite String | SharedWithDatabase | Redis String
type JobStore  = JobsMemory | JobsSqlite String | JobsSharedWithDatabase
```

Every strategy is an ADT. `store = "postgress"` (§1.13's class) becomes a
compile error rather than a runtime fallback to memory.

`config` is a top-level binding the compiler finds (§4.2), so **no entry point
changes signature**. That is what makes this work identically for shapes that
thread no config today.

### 5.2 Sky.Cli — a nightly batch job

```elm
module Main exposing (main, config)

import Sky.Config as Config exposing (Database(..))
import Sky.Env as Env
import Std.Db as Db

config : Task Error Config.Config
config =
    Env.getOr "DATABASE_URL" (Sqlite "ledger.db")
        |> Task.map
            (\db ->
                Config.default
                    |> Config.withDatabase db
                    |> Config.withLog Config.Json Config.Warn
                    |> Config.withJobs Config.JobsSharedWithDatabase
            )

main : Task Error ()
main =
    Db.connect ()
        |> Task.andThen reconcileLedger
```

A CLI job configures its database with the same value and the same builders a
Live app does. `Db.connect ()` keeps its signature (`Std/Db.sky:77`) and reads
the resolved DSN, which now has one more source beneath the environment.

### 5.3 Sky.Http.Server — a headless JSON API

```elm
config : Task Error Config.Config
config =
    Env.require "DATABASE_URL"
        |> Task.map
            (\url ->
                Config.default
                    |> Config.withDatabase (Postgres url)
                    |> Config.withTelemetry (Config.Otlp "http://collector:4317")
                    |> Config.withCsrf False        -- pure Bearer API
            )

main : Task Error ()
main =
    Server.listen 8000 [ Route.get "/health" health ]
```

`Server.listen : Int -> List Route -> Task Error ()` (`Sky/Http/Server.sky:147`)
is unchanged. `Env.require` means a deployment that forgets `DATABASE_URL` fails
at startup with a named error, instead of `detectDriver` silently opening a
SQLite file called `""` (§1.13). `withCsrf False` replaces `[security] csrf`,
which reaches `refreshCsrfEnabled` through the existing hook (§4.3).

### 5.4 Sky.Live and Sky.Tui

Unchanged in shape; the cross-cutting concerns leave the app-shape builders:

```elm
config = … |> Config.withSessions Config.SharedWithDatabase

main =
    Live.app
        (Live.config { init = init, update = update, view = view
                     , subscriptions = subs, routes = routes, notFound = NotFound }
            |> Live.withPort 8000
            |> Live.withStatic "public"
        )
```

`Live.withStore` / `withStorePath` are superseded by
`Config.withSessions` — one typed strategy replacing two stringly-typed knobs,
which is also how `LIVE_TTL`'s three meanings (§1.7) separate into three named
settings.

**Std.Webview needs aligning first.** It is the only shape still on plain
records with pure-Sky update builders (`Std/Webview.sky:30,45,51,66`). It should
adopt the opaque `AppConfig` + kernel-`withX` shape the other three share
(`docs/v0.19/migration-builder-cfg.md` is the precedent), or it will be the one
shape where config composes differently — the exact inconsistency this design
exists to remove.

**Std.Jobs gains its first Sky surface**, via `Config.withJobs`. No new entry
point is needed because `jobsBoot` is lazy (§4.3).

---

## 6. What `sky.toml` becomes

```toml
# sky.toml — build manifest.
[project]
name  = "shop"
entry = "src/Main.sky"
root  = "src"
bin   = "app"

[deps]
# Sky-source dependencies

[deps.go]
"github.com/google/uuid" = "latest"

[build]
embed           = "postgres"   # compile a PostgreSQL bundle INTO the binary
postgresVersion = "16.4"       # which bundle
envPrefix       = "SKY"        # namespace for the residual runtime surface
```

That is the whole file, for every app, at every tier. Nothing here is edited by
an operator and nothing varies between staging and production — the test the
current file fails (§1.15).

### 6.1 Where each current section goes

| Today | Goes to | Why |
|---|---|---|
| `[project]`, bare top-level keys, `[source] root` | `[project]` | compile input; one section, no bare keys |
| `[dependencies]`, `[go.dependencies]` | `[deps]`, `[deps.go]` | compile input |
| `[database] embedded`, `postgresVersion` | `[build]` | R2/R3 |
| `[env] prefix` | `[build] envPrefix` | R4 |
| `[database] path` / `url` | environment, with a code fallback | R6; `withDatabase (Db.fromEnvOr …)` states the fallback |
| `[database] driver` | **deleted** | `build.rs:936-938` — recorded, never emitted |
| `[database]` pool knobs, `isolation`, `txRetry` | environment or code (§4.4) | host-relative |
| `[live] port/static` | code — builders that already exist | app behaviour |
| `[live] store/storePath/ttl` | code — `Config.withSessions` | app behaviour; three meanings separate |
| `[live] input`, `maxBodyBytes` | code — new builders | app behaviour |
| `[jobs] store`, `storePath` | code — `Config.withJobs` | kills the `store_path`/`storePath` trap |
| `[analytics] dbPath`, `retention` | code | app behaviour |
| `[log] format`, `level` | code — `Config.withLog` | app behaviour |
| `[auth]` | **deleted** | read by nothing (§1.11) |
| `[security] csrf` | code — `Config.withCsrf` | app behaviour |
| `[security] env` | environment only | already settled — `build.rs:1168-1173` |

### 6.2 What dies

| Parser | Fate |
|---|---|
| A1 `read_sky_toml_config` `build.rs:911` | **deleted** — no runtime keys remain |
| A6 `db_driver_label` `main.rs:2447` | **deleted** with `[database] driver`; §1.3's live bug dies with it |
| A7 `toml_value`/`parse_toml_scalar` `db_pool_sizing.rs:168,191` | **deleted** or reduced to `--sky-config`; §1.3's divergence dies |
| A4/A5 `parse_toml_entry` / `toml_entry` | **merged** — one `entry` reader |
| A2/A3 `sky_toml_project_key` / `sky_toml_section_key` | **merged** into one manifest reader |
| `pinned_sky_toml` `db_provision.rs:166` | rewritten on the shared reader/writer |
| `is_runtime_config_section` `:1073`, `accepted_config_keys` `:1082`, `inert_key_hint` `:1166` | **deleted** — the accepted set is the manifest's, small enough to be one list |
| the `ffi_ops.rs` dependency scanners | rewritten on the shared reader |

Fourteen parsers become **one manifest reader**, which should use a real TOML
parser: `["go.dependencies"]` already uses quoted-key syntax (`main.rs:1233`),
and `toml_edit` gives the format-preserving writes `sky add` and
`sky db provision --embed` hand-roll today.

---

## 7. Defaults must reproduce today's behaviour

This is the highest-risk part of the design, because its failure mode is
invisible: the app compiles, runs, and quietly does something else.

### 7.1 The rule

Every default is chosen to match **current effective behaviour, including the
defaults we think are wrong**. A default that quietly differs is exactly the
silent behaviour change that was ruled out. Where a different default is wanted,
it is a deliberate, listed, changelogged change — never an accident of
transcription.

### 7.2 The gate — a dual-path fixture matrix

Care is not a mechanism. The check is:

1. Keep the current resolution path alive behind `SKY_CONFIG_LEGACY=1` for the
   transition, so both paths exist in one binary.
2. Build a fixture matrix: every setting × {unset, set in the environment, set in
   `sky.toml`, set via `withX`} — for `withX`, only where a builder exists today.
3. Run each cell under both paths and assert the **effective value is identical**,
   via `--sky-config`.
4. Any difference fails, unless the setting appears in an explicit
   `[[default-changed]]` list with a reason and a CHANGELOG entry. The gate reads
   that list, so an *unlisted* difference cannot pass.

This is falsifiable in the sense `docs/tooling/gate-harness.md` requires: change
one default in the new table, the gate reddens. It ships with that mutation
declared and proven under `--verify-falsifiers`.

A cheaper companion catches transcription errors early: assert each declared
default equals the literal at its current read site — `30*time.Minute`
(`live.go:4013`), `8080` (`live.go:3873`), `"memory"` (`live_store.go:1823`),
`1800`/`86400`/`sky_auth`/`jwt` (`lower.rs:822-825`). It is brittle against
refactors, so it supplements the matrix rather than replacing it.

### 7.3 Where "today's behaviour" is itself a bug

`Live.withTtl` is dead (§1.8): the effective TTL is always 1800 seconds from
`lower.rs:822`, and the builder cannot win. Preserving "today's behaviour"
literally would mean shipping a builder that still does nothing.

The rule that resolves this, and it should be written down: **preserve the
effective value, fix the precedence.** The default TTL stays 1800 seconds, so no
running app changes; `withTtl` starts working, because a builder that does not
work is the defect the whole design exists to remove. This is a listed change in
`[[default-changed]]` — behaviour changes only for an app that *called a builder
that was silently ignored*, which is the smallest and most defensible blast
radius available.

Three known members of this class, all from §1: `Live.withTtl` (dead),
`Live.withStore`/`withStorePath`/`withStatic` (documented backwards in two
places), and `LIVE_TTL`'s three meanings (§1.7) separating into three settings.
Each is listed individually with its old and new behaviour.

---

## 8. Migration

The user has raised migration three times, so it is designed as a first-class
feature rather than a rollout step. Three mechanisms, one source.

### 8.1 One source for the mapping

Both the build-time hint and `sky config migrate` derive from **one**
legacy→new table. Two hand-maintained copies would drift, which is the exact
failure §1.3 proves happens here.

```toml
[[moved]]
from   = { toml = "live.store" }
to     = { code = "Sky.Config.withSessions", ctor = "SharedWithDatabase" }
at     = "0.21.0"
compat = { until = "0.23.0" }

[[moved]]
from   = { toml = "jobs.storePath", alsoSpelled = ["jobs.store_path"] }
to     = { code = "Sky.Config.withJobs", ctor = "JobsSqlite" }
at     = "0.21.0"

[[removed]]
from   = { toml = "database.driver", env = "DB_DRIVER" }
at     = "0.21.0"
reason = "The engine is derived from the DSN's shape (build.rs:936-938). The key never selected anything."

[[removed]]
from   = { toml = "auth.tokenTtl", env = "AUTH_TOKEN_TTL" }
at     = "0.21.0"
reason = "Emitted into every binary's prologue since 0.11 and read by no runtime code (§1.11)."

[[default-changed]]                     # §7.3 — a separate list, a louder message
setting = "live.ttl"
at      = "0.21.0"
was     = "Live.withTtl was silently ignored; the effective TTL was always 1800s"
now     = "Live.withTtl takes effect; the default is unchanged at 1800s"
```

This is a **migration table**, not a configuration schema. It describes history;
the live surface is the Sky type signatures. That distinction is why §10's
schema proposal is no longer needed.

Six artefacts derive from it: the build-time hint (§8.2), the `migrate` rewrite
(§8.3), the boot finding for a renamed env name, the doc note, the CHANGELOG
section, and the compat shim **with its expiry**. `compat.until` is enforced —
the gate fails once the version in `CHANGELOG.md`'s newest heading passes it,
the same mechanism `xtask/tests/docs_state_the_current_version.rs` uses — so a
shim cannot outlive its notice.

The CHANGELOG row is not busywork: `CHANGELOG.md:5-12` records that a version's
section becomes the GitHub Release body and that a heading containing "Breaking"
or "Migration" makes `sky upgrade` print a banner; `print_release_notes`
(`main.rs:320-351`) prints notes for **every intervening release** on a
multi-version jump and flags each such heading (`body_has_breaking`, tested at
`main.rs:4732`).

### 8.2 The build-time hint — this project's migration, where it will be read

The compiler has both halves already: it parses `sky.toml`, so it sees the
legacy keys; it type-checks `main`, so it knows whether a `config` binding
exists. So it prints the specific migration, with **this project's values**:

```
sky.toml has 3 settings that moved into app config in 0.21:

  [live] store     = "postgres"    →  |> Sky.Config.withSessions SharedWithDatabase
  [live] storePath = "$DATABASE_URL"  →  (covered by SharedWithDatabase)
  [log] format     = "json"        →  |> Sky.Config.withLog Json Info

Your app still runs — these are applied as defaults. To adopt them:
  sky config migrate
```

That beats a changelog and beats a doc, because it is this project's migration
and it appears where the user is already looking.

**Noise budget.** The hint fires **only while legacy keys are present**, so it is
self-extinguishing: `sky config migrate` removes the keys and the hint stops
forever. There is **no suppress flag**, deliberately — a suppressed nag is a
forgotten migration, and the alternative to suppressing it is one command. It
prints once per build, not per file, and caps at five settings with a count for
the remainder.

**`sky build` as well as `sky run`.** Both. The dev loop is `sky run`, but the
person performing an upgrade often only sees CI, and CI runs `sky build`. The
self-extinguishing property is what makes this safe: the pipeline noise is
exactly "you have not migrated yet", which is what a pipeline should carry, and
it ends the moment the migration lands. On `sky build` it is a warning block on
stderr, alongside the existing `warning: {w}` channel (`main.rs:786`).

**A project with no legacy keys and no `withX` prints nothing.** That is a
correct, fully-defaulted app. Silence is the signal that there is nothing to do —
and it is what makes the hint's presence meaningful.

**It composes with §7, and the composition is the important part.** The hint must
never fire for a setting whose behaviour has *already* changed — being told to
migrate something that already moved under you is the worst available outcome.
The guarantee is structural: a setting appears in `[[moved]]` **only if** its
default reproduces the old effective value (§7.2's gate proves this, and a
setting that fails the gate cannot be in the list). A setting whose behaviour
deliberately changed appears in `[[default-changed]]` instead, and gets a
different, louder message:

```
⚠ 1 setting CHANGED BEHAVIOUR in 0.21:
    Live.withTtl was silently ignored; it now takes effect.
    Your app calls it with "24h" — sessions were expiring after 30m and
    will now expire after 24h. The default is unchanged.
```

Two lists, two messages, never conflated.

### 8.3 `sky config migrate`

```
sky config migrate               # rewrite sky.toml + write the config binding
sky config migrate --dry-run     # diffs + the equivalence table, no writes
sky config migrate --env         # classify .env; → .env.migrated
sky config migrate --check       # non-zero if anything is pending (CI)
```

It removes each moved `sky.toml` key and adds the equivalent builder call to a
`config` binding in `src/Main.sky`, creating the binding and its import if
absent. Behaviour on a project that has drifted from the scaffold — the normal
case:

| Situation | Behaviour |
|---|---|
| comments, ordering, blank lines | preserved; `toml_edit` edits the document rather than re-rendering it |
| a key with no `moved`/`removed` record | **left in place**, one comment added. Never deleted — we do not know it is ours |
| the same setting in `sky.toml` and already in code, same value | `sky.toml` key removed silently |
| …different values | **hard error**, no write, both locations printed |
| an existing hand-written `config` binding | builders appended to the pipeline, never replacing it |
| `sky.toml` is not valid TOML | refuses, naming the line |

**It proves itself.** Resolve every setting under the old rules from the old
artefacts; resolve under the new rules from the new; assert the effective values
are identical and print every one that moved. The old rules are the code being
deleted, so a hand-copied snapshot would reproduce §1.3 — instead the legacy
reader is generated from the same `[[moved]]`/`[[removed]]` table and deleted
when the compat window expires. The new side is authoritative rather than
modelled: build and ask the binary (`--sky-config`, §4.4).

```
$ sky config migrate --dry-run

  sky.toml   9 keys   6 → code · 2 → [build] · 1 removed

  ✓ 27 settings resolve identically
  ! 1 changes, as declared:  Live.withTtl now takes effect (§7.3)
  ✗ 1 changes, NOT declared:
      db.maxOpenConns   25 → <unset>
      sky.toml line 31 sets it; nothing in the migrated project does.
      This is a bug in the migration table.

  refusing to write. --accept-changes to write anyway.
```

An **undeclared** move fails the tool and, in CI, the release. The check runs
over every `sky.toml` in the repo — 56 examples, `apps/*`, `sky-bundled/*`,
templates, corpus fixtures.

### 8.4 `.env` — classify, do not rewrite

`.env` is gitignored by the scaffold, often holds the only copy of a secret, and
in production is frequently **not the source at all** — the environment comes
from a Kubernetes manifest, a Cloud Run definition, a systemd unit or a CI secret
store. With §3.3's five parser hazards on top, `--env` classifies rather than
rewrites, writes `.env.migrated` and never touches the original without
`--in-place`, and never prints a secret's value:

```
$ sky config migrate --env

  KEEP      DATABASE_URL              read by your config (Env.getOr)
  KEEP      SKY_AUTH_TOKEN_SECRET     read by your code
  RENAME    SKY_METRICS_TOKEN         → SKY_ADMIN_TOKEN
  TO CODE   SKY_LOG_FORMAT=json       now `Config.withLog Json Info`
                                      (env still overrides; delete when ready)
  DELETE    SKY_SOLVER_BUDGET         never read by the runtime
  UNREAD    SKY_LIVE_SESSION_STORE    nothing reads this name

  Your deployment may not use this file:
      sky config migrate --env --format k8s | docker | compose | systemd | shell
```

`TO CODE` is deliberately *not* deleted — the variable still works, so removing
it is the operator's decision after the new binary is deployed. That is what
makes the migration safe in two steps rather than one flag day.

### 8.5 An app that is not migrated

Never silent, in three stages:

- **0.21** — legacy `sky.toml` runtime keys keep working exactly as today, and the
  build hint (§8.2) names the replacement. Old env names keep working and produce
  a boot finding. The app runs unchanged.
- **0.22** — the same keys warn at higher severity and `sky verify` fails on
  them, so CI catches an unmigrated project before a release does.
- **0.23** — `compat.until` expires; the keys become a hard build error naming
  the migration command, and the generated legacy reader is deleted in the same
  commit.

At no point does a key silently change meaning.

---

## 9. The gate that is still needed

The compiler closes "does this setting exist". It does not close "does anything
read it" — `[auth]` was parsed, validated, emitted and ignored for four minor
versions (§1.11), and a builder whose field nothing reads would compile
perfectly. Worse, the Sky↔Go binding is *stringly typed*: `live.go:4013` reads
`stringField(cfg, "Ttl")` and `rt.Field` looks the name up at runtime
(`rt.go:5922-5973`), so renaming a Sky field breaks the read with no error on
either side.

`xtask config-gate` therefore remains, with three assertions:

1. every `withX` builder's underlying field is read somewhere in `runtime-go/`;
2. every `skyGetenv`/`skyLookupEnv` suffix corresponds to a residual-surface
   setting (R4–R6) or a declared legacy name;
3. every setting named in an error string or a startup line exists.

It declares a falsifying mutation — delete a `stringField` read, the gate
reddens — proven under `xtask harness --verify-falsifiers`.

That this is not optional decoration has a precedent in the tree.
`runtime-go/rt/console_app/main.go` is a committed generated file
(`scripts/regenerate-console.sh`) with **no drift gate**:
`xtask/src/harness/bodies.rs:785` records that
"`grep -rn 'regenerate-console\|console_app' .github/` returns nothing", and that
registering a gate "immediately found that the console **did not compile**" —
broken since 2026-07-31.

---

## 10. The rejected alternative — a schema generating readers

Two earlier drafts proposed `config/schema.toml` as the single source of truth,
generating a Rust reader, a Go reader, `docs/sky-toml.md` and a `sky config`
command. It loses on five counts.

1. **It invents a schema language; this reuses one that exists.** A Sky type
   signature already expresses name, type, permitted values (as constructors),
   required-ness and documentation, and is already parsed, checked, formatted,
   completed and rendered by `sky doc`.
2. **It keeps copies in sync mechanically; this has one copy.** Generation makes
   drift *detectable*; having no second artefact makes it *impossible*. §1.3
   argues for the second.
3. **Discoverability was bolted on.** The schema needed `sky config explain`,
   generated Markdown and a new user habit. `sky doc Sky.Config` and LSP
   completion already exist and already never drift — the property AGENTS.md
   relies on for the whole stdlib.
4. **It could not use the type system.** `store = "postgress"` is a hand-written
   enum check; `Postgres` is a constructor.
5. **It kept `sky.toml` as a runtime-config surface** — the layer with no owner
   (§1.5) — and made it nicer rather than removing it.

**What survives:** the verdict that the gate matters more than the schema (§9),
now sharpened into "the compiler is the gate for one class, and an explicit gate
is still required for the other"; the history table, rescoped to migration
(§8.1); generate-and-commit with a drift gate, and the `console_app` precedent
for why it must be gated (§9); and the prohibition on build-script codegen — the
Go side is compiled by the *user's* toolchain from assets embedded via
`include_dir!` (`ffi/src/assets.rs:33`), materialised per build
(`build.rs:1452`), with a content fingerprint baked in so a stale tree cannot
survive (`assets.rs:21-29`).

**When it would come back:** if the residual surface (§4.3) turns out to be
large, `sky.toml` remains a real runtime-config surface and a schema for it is
the right tool again. §12's first risk is the measurement that decides this.

---

## 11. What this does not fix

**Typing proves existence, not effect.** A builder that sets a field the runtime
ignores compiles. §9's gate checks a reader exists; it cannot check the value is
*used*. `[live] input` is the cautionary case — the runtime hardcoded
`"debounce"` behind a `// or "blur"` comment while two examples carried the key,
"so the setting existed on both sides and connected in neither"
(`build.rs:1015-1018`). It is wired now (`live.go:4781`), but a variant that
reads and discards would pass. Closing that needs a behavioural test per setting
— Layer 2 (`apps/manifest.toml`) work.

**Cross-key semantics stay hand-written.** `db_driver_conflict` (`build.rs:894`)
and `driver_for_dsn` (`:860`) mirror `rt.detectDriver` (`db_auth.go:376-389`) by
hand — itself a two-copy drift risk. `detectDriver`'s `default:` arm is a silent
fallback to SQLite, and typing the Sky side does not help: the DSN arrives from
the environment as a string.

**Host-relative validity is out of scope.** Whether `maxOpenConns = 20` is right
depends on the server's `max_connections` and the replica count.

**Provenance is best-effort in a deployed process.** `SetEnvDefault` writes into
the real environment (`dotenv.go:59-66`), so after startup a code default and an
operator `export` differ only by the `seededDefaults` mark (§1.10), which one
consumer reads. Generalising it makes `--sky-config` honest; a value injected by
a wrapper script before the process starts is just "environment".

**Only wrapped settings are overridable** (§3.1). That is the deliberate cost of
explicit precedence.

**Secrets are marked, not protected.** Redaction in listings and diffs; nothing
about a secret reaching a log another way.

**Two config-file parsers remain** — the manifest reader and the `.env` reader,
and the `.env` reader is *already* duplicated across languages with a live
divergence (§3.3 item 5). That divergence is in scope for the same treatment and
is not fixed by moving settings into Sky.

---

## 12. Risks

1. ~~**The residual surface may be larger than §4.3.**~~ **MEASURED — and it
   is.** `xtask config-surface` counts it from the sources into
   `docs/coverage/config-surface.json`; §4.3.1 records what it found and what
   that costs the design. The gate ratchets the count, so the answer cannot
   quietly get worse while the rest of the work proceeds. It is **not** large
   enough to bring §10's schema back: the additions are four `[database]` keys
   and two hand-rolled parsers, not a new layer.
2. **Defaults that do not reproduce today's behaviour** (§7). The failure is
   invisible — the app compiles and runs. The dual-path fixture matrix is the
   only real defence, and it must exist before the first default is written.
3. **The Sky↔Go field binding is stringly typed.** `stringField(cfg, "Ttl")`
   joins the two sides by a name no compiler checks. Extending the builder
   surface multiplies that seam; it needs §9's gate, and possibly extension of
   the existing `abi_guard.rs` discipline to config fields.
4. **`config` as a second entry point is new compiler surface.** A missing or
   ill-typed `Main.config` must produce a good error, and it must be found the
   same way in `sky build`, `sky run`, `sky test` and the LSP.
5. **Explicit env reads put the environment contract in user hands.** A developer
   who wraps nothing produces an app no deployment can tune. The scaffold and the
   strategy helpers have to make the right thing the easy thing, or §3.1's cost
   becomes the common case rather than the edge one.
6. **The strict-parser ambush.** Replacing tolerant hand-rolled scans with a real
   TOML parser will reject manifests that parse today. Scan every `sky.toml` in
   the repo first.
7. **Webview is on a different builder idiom** (§5.4) and must be aligned before
   it becomes the shape where config composes differently.
8. **The gate could be vacuous.** This repository has a documented class of gates
   that recorded PASS while never running, and `console_app` (§9) is a live
   example of ungated generation rotting for a month.
9. **Migration touches everything.** 56 examples, `apps/*`, `sky-bundled/*`,
   templates, corpus fixtures, every doc snippet. `sky config migrate` runs over
   the repo in the commit that lands the deprecations, and
   `scripts/doc-examples.sh` must be green afterwards.
10. **Byte-stability.** Moving defaults out of `sky.toml` changes the emitted
    prologue for every project. `repro`, `golden` and `coerce-floor` baselines
    move together in one commit.
11. **Config in code is code.** A `config` Task can branch on anything, and
    someone will write `if env == "prod" then … else …`, reintroducing at author
    time the deployment coupling the split removes. Whether that is preventable
    in Sky's type system is not established here.

---

## Appendix — verified / inferred

**Verified by reading the cited line on `517c3945`:** §1.1 (workspace deps,
lockfile); §1.2 (all fourteen sites); §1.3 (both `parse_toml_scalar`
implementations, `main.rs:2467`, `rt.go:10219` and the `517c3945` commit
message); §1.4 (`build.rs:1073/1082/1142`; the three `*CfgSet` copies and their
triplicated invariant comments); §1.5 (`build.rs:1070`, `:1155`, `:1166-1180`);
§1.6 (`env_prefix.go` in full, `observability.go:347-360`, the three
dual-namespace pairs); §1.7 (all three `LIVE_TTL` readers with their defaults,
`csrf_middleware.go:68-69`); §1.8 (`live.go:3859-3873`, `live.go:3992-4013`,
`live_store.go:307-325`, `live_store.go:1753-1759`, `lower.rs:822`,
`Std/Live.sky:167-168` — the dead `withTtl` and both backwards docstrings);
§1.9 (`env_prefix.go:107-119`, the three `onEnvPrefixChange` registrations);
§1.10 (`dotenv.go:47-64,96`, `live.go:3859`); §1.11 (the searches proving no
`AUTH_*` reader, `startup_report_test.go:110-111`); §1.12 (counts recomputed by
grep including the suffix form); §1.13 (`live_store.go:1823-1825`,
`jobs_kernel.go:312-318`, `analytics_store.go:211-229,286-302,346-353`,
`db_auth.go:376-389`, `db_pool.go:502-745`); §1.14 (`console_auth.go:74-80`,
`console_auth_v2.go:448`, `startup_report.go:63-74`, `otel.go:57-59`); §1.15
(`main.rs:1196-1235`); §1.16 (`docs/sky-toml.md` at the cited lines,
`docs/skyauth/overview.md:198`, `docs/skylive/pubsub-design.md:1109`); §2 (every
module surface cited, `live_config.go:16-69`, `rt.go:5922-5973`,
`lower.rs:275-277,1426,1474,1540-1591`, `docs/v0.19/migration-builder-cfg.md`);
§3.3 (`dotenv.go:106,162,187-224`, `db_pool_sizing.rs:203-224`); §4.1 (the
fourteen `rt` `init()`s and the five that touch config; `lower.rs:795-827`;
`lower.rs:2386-2440`); §4.3 (`build.rs:569-577,989,1622-1630`,
`db_provision.rs:154`, `jobs_kernel.go:86,206,233,261`); §4.4
(`db_pool_sizing.rs:126-143`); §5.1 (`Std/Config.sky:1-47` proving the name is
taken; `sky-stdlib/Sky/` contents); §8.1 (`CHANGELOG.md:5-12`,
`main.rs:320-351`, `main.rs:4732`); §9 (`bodies.rs:780-790`); §10
(`ffi/src/assets.rs:21-33`, `build.rs:1452`).

**Inferred, not executed:** the `sky db reset`/`drop` prompt mislabelling
(§1.3), read from `db_driver_label` and its caller at `main.rs:2538`; that
`Live.withTtl` is unreachable (§1.8) — read from `lower.rs:822`,
`live.go:4013` and `parseTTL`'s argument order, but not reproduced by running a
program; that `Server.listen` and `Db.connect` need no signature change (§5.2,
§5.3), which follows from `config` being applied in the preamble but was not
confirmed against their implementations; that `ApplyConfig` can be emitted ahead
of `MaybeStartEmbeddedPostgres` (§4.2), which follows from `config` being an
ordinary top-level binding and from `lower.rs:2386-2412`'s stated reason for
that call's placement; that `sky config env` can be derived by static extraction
(§3), which needs a pass that does not exist and can only see literal names.
Nothing in this document was executed, in line with the read-only,
no-heavy-build constraint.

**Corrected from earlier audits:** "seven parsers" is right for scalar readers
and undercounts the total (fourteen, §1.2); "31 documented names with no reader"
recomputes to **25** and "48 read names in no live doc" to **46** (§1.12); the
claim that the DB pool knobs are undocumented is **false** (§1.12). And an
earlier draft of this document rejected typed configuration on a circularity
argument drawn far too widely — it kills a design in which *everything* moves to
code, and says nothing about one whose residue is chosen to include what CLI
verbs need. The corrected, narrow form is §4.3 R3.

---

## Appendix B — the measured surface

`docs/coverage/config-surface.json`, regenerated by `xtask config-surface` and
verified current by the registered gate of the same name. Every number this
document quotes about *counts* comes from there; the file:line evidence in §1
was read by hand and is cited individually.

The gate asserts three things and ratchets four counts. Its counts are DEFECT
counts, so the ratchet runs the opposite way from the denominator ledger: a
fall is free, a rise must be written down as a `[[config-surface-rise]]` stanza
in `docs/coverage/removals.toml` pinned to an exact `from`/`to`.

| Count | Meaning | Falls when |
|---|---|---|
| `pre_binary_surfaces` | `sky.toml` surfaces a CLI verb reads with no binary in existence — §4.3's residue, measured | a setting moves into app config, or a verb learns to ask the binary |
| `seeded_without_reader` | env suffixes the compiler seeds into every prologue that nothing under `runtime-go/` reads — §1.11's class | the emission is deleted, or a reader is wired |
| `documented_without_reader` | `SKY_*` names in live docs that appear nowhere else in the tracked tree | the doc is corrected, or the name is implemented |
| `read_without_doc` | names the runtime reads that no live doc mentions | the name is documented |

**What it cannot see, and says so.** A reader that names its key with anything
the scan cannot resolve to a string is reported as an UNRESOLVED READ SITE and
the residual count is declared a **lower bound** — it is never quietly
under-counted. Two shapes are resolved rather than refused: a key named by a
`const … &str` (which is how `db_provision.rs` names the PostgreSQL pin), and a
suffix read through a wrapper such as `dbEnvInt` rather than a bare
`skyGetenv`. A NEW wrapper fails the gate until it is declared, because an
invisible read makes the unread-seed count wrong in both directions — reporting
a defect that is not there, or missing one that is.

**What it cannot see at all.** Whether a setting that IS read has any *effect*.
That is §11's first limitation and it is unchanged: `[live] input` existed on
both sides and connected in neither, and a variant that reads a value and
discards it would pass every assertion here. Closing that needs a behavioural
test per setting — Layer 2 work, not measurement.
