# Configuration architecture — one schema, generated readers, `sky config`

> **Design record, not a plan of record.** It argues a position, records what
> was verified at the time of writing, and names what it does not solve. Nothing
> here is scheduled. Written against `feat/embedded-postgres` @ `0fdee2c4`.

## Why this lives in `docs/tooling/`

The three things this design produces are all tooling artefacts:

- a CLI verb (`sky config`) — a sibling of `docs/tooling/cli.md`;
- a generation-and-drift gate — a sibling of `docs/tooling/gate-harness.md`;
- a generated user reference — `docs/sky-toml.md`, which becomes an output.

It is **not** in `docs/rust-rewrite/`. That directory is a numbered narrative of
the compiler pipeline (00 goals → 14 narrowing taxonomy), and AGENTS.md names it
"the FIRST source consulted on any compiler change". Configuration is barely a
compiler change: exactly one pipeline site is touched (the prologue `init()` in
`rust/crates/lower/src/lower.rs:795`) and everything else is CLI verbs, the Go
runtime, and documentation. Filing it as `docs/rust-rewrite/15-…` would imply a
pipeline stage that does not exist and would put it in the wrong reading order
for the people who need it.

---

## 1. What is actually true today

Every claim in this section was read at the cited line on `0fdee2c4`. Claims
marked **INFERRED** are reasoning over what was read, not read directly.

### 1.1 There is no TOML parser in the workspace

`rust/Cargo.toml`'s `[workspace.dependencies]` (lines 24–44) lists
`la-arena`, `smol_str`, `indexmap`, `annotate-snippets`, `rowan`, `logos`,
`salsa`, `serde`, `serde_json`, `include_dir`, `tower-lsp`. No `toml`, no
`toml_edit`, no `basic-toml`; `rust/Cargo.lock` has no `toml*` package.
`rust/crates/xtask/src/coverage_ledger.rs:1153` states the policy in as many
words: hand-parsed rather than pulling in a TOML dependency.

Consequence: **every** read of `sky.toml` in the workspace is a hand-rolled
line scan.

### 1.2 Fourteen independent hand-rolled parsers

Seven read scalar `key = value` pairs; seven more scan structure (dependency
tables, section presence, in-place edits). The inventory's "seven" is right for
the first class and undercounts the whole.

**Scalar readers**

| # | Site | Extracts | Section rule | Value rule |
|---|---|---|---|---|
| A1 | `rust/crates/project/src/build.rs:911` `read_sky_toml_config` | the whole runtime-config surface | `starts_with('[')` + `find(']')` | `parse_toml_scalar` (`build.rs:841`) |
| A2 | `rust/crates/project/src/build.rs:1164` `sky_toml_project_key` | `bin`, `root` | `starts_with('[') && ends_with(']')` | inline, `build.rs:1191-1196` |
| A3 | `rust/crates/project/src/build.rs:1221` `sky_toml_section_key` | any `[section] key` | `find(']')` | `parse_toml_scalar` |
| A4 | `rust/crates/sky/src/main.rs:660` `parse_toml_entry` | top-level `entry` | breaks at first `[` | own closure, `'` and `"` |
| A5 | `rust/crates/sky/src/main.rs:4472` `toml_entry` | `entry` | **none** — greps anywhere | own closure, `'` and `"` |
| A6 | `rust/crates/sky/src/main.rs:2447` `db_driver_label` | `[database] driver/path/url` | `starts_with && ends_with` | `trim_matches('"')` |
| A7 | `rust/crates/sky/src/db_pool_sizing.rs:168` `toml_value` | `[database] maxOpenConns`, `[env] prefix` | `find(']')` | own `parse_toml_scalar`, `db_pool_sizing.rs:191` |

**Structural scanners**: `project/src/ffi_ops.rs:1187` `read_dependencies`,
`:1037` `upsert_dependency`, `:1110` `remove_dependency`, `:1164`
`is_sky_package_root`; `sky/src/db_provision.rs:166` `pinned_sky_toml`
(locate-then-edit rewrite); `xtask/src/coverage_ledger.rs:1124` `read_sky_toml`;
`xtask/src/build_run_gate.rs:735` `declares_real_deps`. Plus a section-header
grep at `sky/src/main.rs:3679` `check_auth_secret`.

That is **four different rules for recognising `[section]`** —
`find(']')` (A1/A3/A7), `starts_with && ends_with` (A2/A6, and
`coverage_ledger.rs`), `contains(']')` (`db_provision.rs:179`), and exact byte
equality against `"[database]"` (`coverage_ledger.rs`) — and **three different
rules for a scalar value**.

### 1.3 The divergence is not theoretical, and it beat a deliberate attempt to prevent it

`rust/crates/sky/src/db_pool_sizing.rs:189-190` says, of its own
`parse_toml_scalar`:

> Strip an inline comment and matching quotes — `project::build`'s
> `parse_toml_scalar`, kept behaviourally identical for the reason above.

It is not identical. Compare the two on `maxOpenConns = "25"  # note`:

- `build.rs:841` sees the leading `"`, takes text up to the next `"`, returns
  `25`.
- `db_pool_sizing.rs:191` sees a leading `"`, therefore **skips** comment
  stripping, then `trim_matches('"')` — which removes leading/trailing `"`
  characters, and the string ends in `e`. Returns `25"  # note`.

They also disagree in the other direction: `build.rs:841` does not handle `'`
at all, so `maxOpenConns = '25'` returns `'25'` there and `25` in
`db_pool_sizing.rs`.

This is the whole argument in one place. Two functions, adjacent in the
codebase, one explicitly written to mirror the other, with a doc comment
asserting they match — and they diverge on exactly the input the original was
fixed for. **Discipline was applied and it failed.** The conclusion is not
"apply more discipline".

The same bug class is still live at `rust/crates/sky/src/main.rs:2467`:

```rust
let (key, val) = (k.trim(), v.trim().trim_matches('"').to_string());
```

`db_driver_label`'s doc comment (`main.rs:2444-2446`) claims it "Mirrors
`read_sky_toml_config`'s `[database]` handling". With
`driver = "postgres"  # prod` it returns `postgres"  # prod`. Its only caller is
`main.rs:2538`, the confirmation prompt for `sky db reset` / `sky db drop` —
so the destructive-operation prompt can name the wrong engine. **INFERRED**
(read the code path; did not execute it).

`db_driver_label` also reads `"sky.toml"` relative to the process cwd
(`main.rs:2448`), where every other reader takes a project directory.

### 1.4 The codebase has already discovered it wants a schema, twice

`rust/crates/project/src/build.rs:1073` `is_runtime_config_section` lists the
sections whose keys are checked; `build.rs:1082` `accepted_config_keys` lists
the keys per section, with a doc comment saying it is "kept adjacent so the two
cannot drift apart silently". Both are hand-maintained restatements of the
`match` arms at `build.rs:934-1062`. That is three copies of the same fact in
one file, and the file says so.

`build.rs:1142` `unknown_config_keys` then turns the third copy into a build
warning. Its doc comment (`build.rs:1119-1137`) records what the missing check
had already cost: `examples/08-notes-app` and `examples/12-skyvote` both set
`[auth] method`, `secret`, `session_ttl`, `email_verification` — none parsed,
three not keys at all, and `session_ttl` is `tokenTtl` misspelled, so both
examples advertise a 24-hour session and get the default.

### 1.5 `[security]` is a section the runtime believes in and nothing implements

`is_runtime_config_section` (`build.rs:1073`) lists
`live | database | auth | log | analytics | jobs | env`. `[security]` is absent,
so its keys fall through `build.rs:1057`'s `_` arm and are dropped **without
even the unknown-key warning** — the warning only fires for recognised sections.

Meanwhile the runtime documents the section as real:

- `runtime-go/rt/csrf_middleware.go:111`: "The sky.toml `[security] csrf = false`
  toml-side plumbing routes through here too once it lands."
- `runtime-go/rt/csrf_middleware.go:120-121`: `SetCsrfEnabled` is "Called from
  the runtime startup path when sky.toml `[security] csrf = false`."
- `runtime-go/rt/observability.go:295`: `productionMode` is set by
  "sky.toml `[security] env = "production"` (explicit, wins)".
- `runtime-go/rt/observability.go:336-337`: `SKY_ENV` is "the namespaced
  variant the compiler emits from `sky.toml [security] env = ...`".

The compiler emits nothing of the kind. Grep for `"security"` across
`rust/crates/**/*.rs` returns no match; grep for `[security]` across live `docs/`
returns no match. A user following the runtime's own comments writes a section
that is silently discarded.

### 1.6 The prefix scheme and the literal scheme are two namespaces, and the security gate is on the wrong one

`runtime-go/rt/env_prefix.go` defines the internal namespace:
`skyEnvName(suffix)` (`:80`) = `envPrefix + "_" + suffix`; `skyGetenv` (`:94`)
and `skyLookupEnv` (`:86`) read through it; `SetEnvPrefix` (`:50`) is emitted
from `sky.toml [env] prefix`; `SetSkyDefault` (`:107`) seeds a default under the
same prefix.

There is no fallback inside `skyGetenv`. `skyGetenv("LIVE_PORT")` is exactly one
`os.Getenv` of `<PREFIX>_LIVE_PORT`; under `prefix = "FENCE"`, `SKY_LIVE_PORT` is
not consulted and bare `LIVE_PORT` never is. Only two names in the whole
prefixed namespace have a second spelling: `LIVE_STATIC_DIR` → `STATIC_DIR`
(`live.go:3976/3983`) and `DB_PATH` → `DATABASE_URL` (`db_auth.go:242-247`).

**Three settings are read through both namespaces at once.** Under a custom
prefix each becomes two different variables gating one thing:

| Setting | Prefix-aware read | Hardcoded read |
|---|---|---|
| console admin token | `skyGetenv("ADMIN_TOKEN")` `console.go:403` | `os.Getenv("SKY_ADMIN_TOKEN")` `console_auth.go:74` |
| Live base path | `skyGetenv("LIVE_BASE_PATH")` `live.go:3966` | `os.Getenv("SKY_LIVE_BASE_PATH")` `console.go:142,287`, `console_auth_v2.go:398` |
| synchronous-commit twins | `ANALYTICS_SYNCHRONOUS_COMMIT` `analytics_writer.go:227` | `SKY_TELEMETRY_SYNCHRONOUS_COMMIT` `telemetry/persist.go:842` |

The last pair carries a comment at `analytics_writer.go:230-232` asserting the
two "cannot drift in meaning". They already drift in *namespace*.

**And the production predicate is on the literal side.** There are three
production notions, not one:

```go
// runtime-go/rt/rt.go:10203
func isProd() bool { return skyGetenv("ENV") == "prod" }

// runtime-go/rt/observability.go:333
func productionFromEnv() bool {
    envFlag := strings.ToLower(os.Getenv("ENV"))
    if envFlag == "" { envFlag = strings.ToLower(os.Getenv("SKY_ENV")) }
    if envFlag == "" { return false }
    switch envFlag { case "dev", "development", "local": return false }
    return true
}
```

plus `isProductionMode()` (`observability.go:310`), an `atomic.Bool` set from the
Live startup path (`live.go:4198`, `rt.go:9468`). The first two disagree in four
independent ways: **namespace** (`<PREFIX>_ENV` vs literal `ENV`/`SKY_ENV`, so
under a prefix they can never agree), **matching rule** (exact `"prod"` vs a
deny-list of `dev`/`development`/`local`), **case** (one lowercases, one does
not), and **priority** (with `ENV=dev` and `SKY_ENV=prod`, `productionFromEnv`
is false and `isProd` is true).

The consequence for a real deployment: `SKY_ENV=production` — the spelling
AGENTS.md's production gate asks for — turns on the console/metrics auth gate
and leaves `isProd` false, so Secure cookies (`securifyCookieAttrs`,
`rt.go:10209`) and the compact production panic log (`logPanicFrame`,
`rt.go:10233`) stay off.

The narrow fix is being made separately. The point here is the *cause*: nothing
declares, in one place, that `ENV` is one setting with one name, one namespace
rule and one matching rule.

### 1.7 `SetSkyDefault` mutates the process environment, at build-baked values

`runtime-go/rt/env_prefix.go:107-119`: `SetSkyDefault(suffix, value)` calls
`SetEnvDefault(skyEnvName(suffix), value)` — set-if-unset — then re-runs
`envPrefixHooks` because package-level env-derived vars (`logJSON`,
`logThreshold`) were evaluated before the generated `init()` ran.

`rust/crates/lower/src/lower.rs:795-827` emits the prologue: `SetEnvPrefix`
first (`:811-813`), `SetPortDefault` (`:814`), then one `SetSkyDefault` per
`cfg.extra_defaults` entry (`:819-821`), then four hardcoded fallbacks
(`:822-825`): `LIVE_TTL=1800`, `AUTH_TOKEN_TTL=86400`, `AUTH_COOKIE=sky_auth`,
`AUTH_DRIVER=jwt`.

Two consequences that a design must respect:

1. **`sky.toml` runtime values are compiled in.** Editing `sky.toml` and
   restarting a built binary changes nothing; only a rebuild does. Nothing in
   the CLI says so today.
2. **Provenance survives only because one feature needed it.**
   `SetEnvDefault` records every name it seeds in a `seededDefaults` side-table
   (`runtime-go/rt/dotenv.go:47-50`, marked at `:64`), and `.env` writes go
   through `setEnvRaw` (`:96,167`) which *clears* the mark — so a `.env`-set
   value counts as operator-set and a `sky.toml`-set value does not. Exactly one
   consumer reads it: `resolveLivePort` via `isSeededDefault`
   (`live.go:3853-3874`), so that `Live.withPort` can outrank a manifest default
   but not an operator's. For every other setting the distinction is discarded,
   and after `init()` a manifest default and an `export` are indistinguishable.

   This matters twice over: it is the mechanism a "where did this come from?"
   feature needs, and it **already exists**. Generalising it is cheaper and
   safer than inventing a parallel provenance channel.

`cfg.extra_defaults` is a `Vec` filled in **file order** (`build.rs:934-1062`),
so the emitted Go byte sequence depends on the order of keys in the user's
`sky.toml`. Deterministic for a fixed file — `xtask repro`
(`rust/crates/xtask/src/repro_gate.rs`) is satisfied — but it means a cosmetic
reordering of `sky.toml` changes the emitted program.

### 1.8 The manifest the scaffold writes is itself the legibility complaint

`rust/crates/sky/src/main.rs:1220-1235` writes, for a default `sky init`:

```toml
name    = "myapp"
version = "0.1.0"
entry   = "src/Main.sky"
bin     = "app"

[source]
root = "src"

[live]
port  = 8000
store = "memory"          # dev sessions (memory | sqlite | postgres | redis)

[database]
driver = "sqlite"
path   = "app.db"

# ── PRODUCTION (scaling / multi-instance): one Postgres for everything.
# [live]
# store = "postgres"
# [database]
# driver = "postgres"      # falls back to DATABASE_URL
# [analytics]
# retention = "180d"

# [auth]            # Std.Auth (uncomment to use)
# driver     = "jwt"
# cookieName = "sky_sid"
```

Read as a specimen, that file has six distinct problems:

1. `name`/`version`/`entry`/`bin` are **bare top-level keys**, while
   `docs/sky-toml.md:48-69` documents them as `[project]` keys. Both work
   (`build.rs:1185` accepts scope `""`, `project`, or `source`); the file the
   tool writes disagrees with the reference the tool ships.
2. `[source] root` is a whole section for one key, and that key is documented
   under `[project]`.
3. `port` is readable at top level *and* as `[live] port` (`build.rs:935` and
   `build.rs:1016`), both landing in `cfg.port`.
4. `[database] driver` is decorative: `build.rs:947-950` records it and never
   emits it, because "Nothing in runtime-go reads DB_DRIVER / SKY_DB_DRIVER; the
   driver comes from the DSN's shape". Its only effect is to be checked for
   contradiction (`db_driver_conflict`, `build.rs:894`). The scaffold puts it in
   front of every new user as if it selected something.
5. The production path is taught by **commented-out duplicate section headers**.
   `# [live]` / `# [database]` re-open sections that already appear above. TOML
   would reject the duplicate tables if uncommented as written; the user must
   understand they are meant to merge the keys upward.
6. `cookieName = "sky_sid"` — but `sky_sid` is the Sky.Live *session* cookie the
   production gate keys sticky sessions on (AGENTS.md, "Production gate"), and
   the auth cookie's own default is `sky_auth` (`lower.rs:824`). The scaffold
   suggests colliding them.

And the section names describe **compiler modules, not decisions**. The four
places a user configures "where state lives" are `[database] path`,
`[live] store`, `[jobs] store`, `[analytics] dbPath` — one decision, four
sections. `main.rs:1205` and `main.rs:1295` (the `.env.example` template) both
tell the user the production shape is *one* URL wiring "app data + sessions +
analytics + telemetry into one DB". The manifest does not let them express that.

### 1.9 The `[auth]` block is write-only, and the tree already knows

Parsed at `build.rs:1045-1047` (`cookieName` → `AUTH_COOKIE`, `tokenTtl` →
`AUTH_TOKEN_TTL`, `driver` → `AUTH_DRIVER`), listed as accepted at
`build.rs:1106`, emitted at `lower.rs:819` and defaulted at `lower.rs:823-825`.

Read by nothing. The searches performed: `AUTH_` across `runtime-go/` hits two
files, both comments (`env_prefix.go:5,16`, `startup_report.go:70`); no
`skyGetenv`/`skyLookupEnv` call takes an `AUTH_*` suffix; no `.sky` file
mentions the three names; a whole-repo literal search finds only those comments,
`rust/target/**` build copies, and the retired emitter at
`legacy-haskell-compiler/src/Sky/Build/Compile.hs:7972-7974`. Three keys are
parsed, validated, emitted into every binary's prologue, and consumed by no one.

The sharpest detail: `runtime-go/rt/startup_report_test.go:110-111` asserts the
startup banner must **not** name `SKY_AUTH_TOKEN_SECRET`, "which no runtime code
reads". The repository has a *test* protecting the knowledge that these are not
runtime settings, while `docs/sky-toml.md:182-186` still tabulates them as
configuration with effect. The fact is known, tested, and undocumented.

### 1.10 The doc/code reconciliation, measured

Counted mechanically over live `docs/**` (excluding `docs/history/`) against
non-test readers in `runtime-go/**` and `rust/crates/**`, with the reader set
taken as literal `SKY_*` strings **unioned with the suffix form** so the
prefixed namespace is not missed:

- **25 documented names have no reader.** Six appear nowhere in the repo at all
  (`SKY_LSP_DEBUG`, `SKY_LSP_TRACE`, `SKY_SUBAPP_VERBOSE`, `SKY_ANALYTICS_DEBUG`,
  `SKY_LIVE_MAX_SUBS_PER_SESSION`, `SKY_ADT`); three are the `[auth]` block
  above; three are documented and self-declared dead (`SKY_AUTH_SECRET`,
  `SKY_DB_DRIVER`, `SKY_DB_URL`); five are read only by the retired Haskell
  compiler (including `SKY_SOLVER_BUDGET`, which `docs/sky-toml.md:559-560`
  still describes as "read by the Haskell compiler itself" — as does
  `runtime-go/rt/env_prefix.go:24-25`); eight only by shell scripts and CI.
- **46 read names appear in no live doc** (44 if AGENTS.md counts as one),
  concentrated in whole undocumented subsystems: 18 console hub + spool names
  (`exporter.go:297`, `exporter_spool.go:140`), 5 HTTP timeouts
  (`rt.go:9485-9488`, `stdlib_extra.go:1322`), 13 toolchain names.
- **39 distinct suffixes are read** through the prefixed namespace; **42** are
  seeded. The three-name gap is `[auth]`.

The earlier inventory's figures of 31 and 48 are close but were not reproducible;
these are. **One inventory claim is false and instructive**: the DB pool knobs
are *not* undocumented. `maxOpenConns` / `maxIdleConns` / `connMaxLifetime` /
`connMaxIdleTime` / `isolation` / `txRetry` are documented at
`docs/sky-toml.md:261-266` and `:308-311` and read at
`runtime-go/rt/db_pool.go:514-517,575,592`. The claim arose because
`grep SKY_DB_MAX_OPEN_CONNS docs/` returns nothing: the docs only ever write
these as `<PREFIX>_DB_MAX_OPEN_CONNS`, while the runtime's own warning prints
the *resolved* literal via `skyEnvName(…)` (`db_pool.go:525`,
`pg_embed_conf.go:331`). **The name an operator reads in a warning is not
findable in the documentation.** That is a discoverability defect in its own
right, and it fooled an audit.

### 1.11 `docs/sky-toml.md` has drifted from itself

- The "All sections at a glance" table (`:30-39`) lists eight sections. `[jobs]`
  is documented at `:466` and absent from the table. `[analytics]` is a real
  parsed section (`build.rs:997-1006`, `:1108`) with **no section anywhere in the
  file** — its two keys appear only as env vars, filed under `[database]` at
  `:436-437`. `[security]` and `[source]` appear in neither.
- `[database]` (`:210-462`) is five unrelated topics under one heading, two of
  which document no `[database]` key at all: "Analytics and telemetry writes"
  (`:425`) and "Garbage collection" (`:439`) — the latter opening with "There is
  no sky.toml knob for the collector" while sitting inside a sky.toml section
  reference.
- `:203` states "Keys are **camelCase**"; `:482` accepts `store_path` "because
  that is the name the runtime's own error message used". The rule and its
  exception are one section apart.
- The `[auth]` example at `:174-180` shows a `secret` key, then `:188-201`
  retracts it over fourteen lines including a retraction of an earlier
  retraction.
- Two live docs disagree on a type: `docs/sky-toml.md:177` gives
  `tokenTtl = 86400` (seconds), `docs/skyauth/overview.md:198` gives
  `tokenTtl = "24h"` (a duration string).
- `docs/skylive/pubsub-design.md:1109` documents a `[live.broker]` section with
  `kind` / `url` / `prefix`. No parser arm exists, and `[live.broker]` is not in
  `is_runtime_config_section`, so its keys are dropped with no warning — the
  same silence as `[security]`.
- The precedence contract is stated twice, at `:41-44` and `:572-586`.

---

## 2. Testing the premise

The proposal on the table: **one declarative schema as the single source of
truth**, generating the Rust reader, the Go reader, the docs, and a `sky config`
verb. Below is the case against it, taken seriously, and what survives.

### 2.1 Objection: a shared parser would fix §1.2 and §1.3 without codegen

Largely true. Fourteen parsers collapsing to one is a **refactor**, not a
generation problem, and it delivers the "correctness" third of the ask on its
own. If the Rust compiler were the only consumer, `accepted_config_keys`
(`build.rs:1082`) grown into a proper static table would be the whole answer and
this document would be two pages shorter.

What defeats it is **fan-out across languages**. The same facts are needed by:

- Rust, at compile time (parse, validate, emit `SetSkyDefault`);
- Go, at run time (read the value, know its default, know its name under the
  active prefix, report itself);
- Markdown, as the user reference;
- the `sky init` scaffold;
- `sky config`.

A Rust `static` cannot be read by Go or by a Markdown file. Something must cross
the language boundary. So the question is not "schema or no schema" but **what
crosses, and when**.

### 2.2 Objection: then let the runtime read a serialised schema

Rejected. The runtime is compiled into the user's binary from an embedded copy
of `runtime-go/` (`rust/crates/ffi/src/assets.rs:33` embeds the tree via
`include_dir!`; `rust/crates/project/src/build.rs:1452` materialises a pruned
copy into the out dir per build). Handing it a data file at run time means
either another `go:embed` payload or a file the deployment must carry, plus a
parse on every process start, plus a new failure mode ("schema file missing")
in a binary whose selling point is that it is one file. The runtime does not
need to *interpret* a schema; it needs a table of constants. Generate the table.

### 2.3 Objection: build-script codegen will break the build order

It would, and it is not proposed. Three facts force the decision:

1. The Go side is compiled by the **user's** toolchain, from assets embedded in
   the `sky` binary. A `build.rs` in the Rust workspace cannot run then.
2. `rust/crates/ffi/src/assets.rs:21-29` bakes a content fingerprint of the
   embedded tree into the crate's compile command precisely because
   `include_dir!` does not register its files as cargo dependencies. A generated
   Go file that is *not* on disk when `ffi` is compiled is not in the binary.
3. `xtask repro` (`rust/crates/xtask/src/repro_gate.rs`) pins byte-identical
   emission across fresh processes. Anything generated per build is new surface
   for that gate.

Therefore: **generate and commit, with a drift gate.** `cargo run -p xtask --
config-gen` writes the outputs; `cargo run -p xtask -- config-gen --check`
regenerates into memory and fails on any difference. This is the pattern the
repo already uses for `xtask denominators` and `xtask coverage-ledger`
(`docs/coverage/`), and the shape AGENTS.md describes for
`kernel_api_covers_registered_kernel_functions`. Cost to the compiler build:
zero. Cost to `xtask repro`: zero, because the generated files are inputs, not
outputs. Cost to a contributor: one command, enforced by CI.

### 2.4 Objection: the compiler gains a TOML dependency

It should, and this is a real cost worth stating plainly. Hand-rolled scanning
cannot be *correct* — the ask's first word — because `sky.toml` is TOML, and
TOML has multi-line strings, multi-line arrays, dotted keys, quoted keys
(`["go.dependencies"]` is already in the scaffold, `main.rs:1233`), inline
tables, and escapes. Every current reader would mis-read a value spanning two
lines, and several would mis-read a `#` inside a string.

Recommendation: `toml_edit` in one new `config` crate. It parses *and* writes
format-preservingly, which is what `sky config set` needs and what
`db_provision.rs:166` `pinned_sky_toml` hand-rolls today. `basic-toml` is
smaller but read-only, so it would leave the write path hand-rolled — the exact
split that produced §1.3.

The **generated** artefacts depend on nothing: the Rust output is a `static`
array, the Go output a `[]ConfigEntry` literal. Only the schema *reader* (xtask)
and the `sky.toml` *reader* (the config crate) touch `toml_edit`.

### 2.5 Objection: a schema cannot express what a config actually needs

Partly true, and it bounds the design rather than defeating it. Three things
resist declaration:

- **Cross-key predicates.** `db_driver_conflict` (`build.rs:894`) compares a
  declared driver with the DSN's shape via `driver_for_dsn` (`build.rs:860`),
  which is itself a hand-maintained mirror of `rt.detectDriver`. That is code.
- **Host-relative sanity.** `db_pool_sizing.rs` reproduces the runtime's pool
  arithmetic to advise on `max_connections`. That is code.
- **Whether a reader exists at all.** A schema entry saying "`[auth] tokenTtl`
  is read at runtime" is a *claim*. §1.4 shows the claim can be false for years.

The answer is not to widen the schema into a programming language. It is to keep
the schema to *facts about names* and put the predicates in code that the schema
**indexes** — and, crucially, to add a gate that checks the one claim the schema
makes which is checkable: that every entry declared `scope = "runtime"` has a
matching `skyGetenv`/`skyLookupEnv` call in `runtime-go/`, and vice versa. That
gate is the mechanised answer to the `[auth]`, `[jobs]`, `[live] input` and
`[security]` classes, and it is worth more than the codegen.

### 2.6 Verdict

**The single-schema approach is right, with two amendments.**

1. It is right for *names, types, defaults, scope, prefixability, deprecation,
   descriptions* — the facts currently duplicated up to nine ways. Generation
   makes drift structurally impossible, which is exactly the property
   `sky doc <Module>` already gives the stdlib.
2. **Amendment one — the schema is not the deliverable; the gate is.** If only
   one thing ships, ship the reader/schema reconciliation gate (§2.5). It closes
   the failure class that actually reached users (config that looks set and does
   nothing). Codegen without that gate produces a beautifully consistent
   description of a runtime that may ignore all of it.
3. **Amendment two — no schema interpreter anywhere at run time.** The schema is
   consumed only by `xtask`. Rust and Go each receive a flat constant table.

The premise's own framing — "drift becomes impossible by construction rather
than by discipline" — is confirmed by §1.3 in the strongest available form: a
deliberate, documented, careful attempt at discipline failed inside one release.

---

## 3. The schema

### 3.1 Location and format

`config/schema.toml` at the repo root. TOML, because it is the language of the
file it describes, needs no new tooling, and is diffable in review. Consumed by
`xtask` only.

### 3.2 What an entry declares

| Field | Meaning |
|---|---|
| `key` | Canonical dotted path, e.g. `data.sessions.store`. The identity. |
| `type` | `string` \| `int` \| `bool` \| `duration` \| `bytes` \| `enum` \| `path` \| `dsn` |
| `values` | For `enum`: the permitted set. Generates the validator and the docs list. |
| `default` | The value when nothing sets it. Absent = no default (unset is meaningful). |
| `scope` | `toolchain` (CLI only, never reaches the binary) \| `runtime` (seeded into the binary, overridable by env) \| `emit` (shapes emission itself, e.g. the prefix) |
| `env.suffix` | The internal env name for `scope = "runtime"`. |
| `env.prefixable` | Whether `[env] prefix` applies. Default `true`; `false` is a deliberate, reviewable exception. |
| `env.aliases` | Unprefixed names consulted as a fallback, in order (`DATABASE_URL`, `PORT`, `REDIS_URL`). |
| `secret` | Redact in every listing and log. |
| `requiredWhen` | A closed predicate: `{ key = …, equals = … }`. Not an expression language. |
| `deprecated` | `{ since = "0.20.3", use = "data.db.url" }` — generates the migration and the warning. |
| `tier` | `prototype` \| `production` \| `both` — drives `sky config` grouping and the scaffold. |
| `summary` | One line. Generates the docs row and `sky config explain`. |
| `since` | Version introduced. |

Deliberately **not** in the schema: validation predicates that read another
key's *value shape* (§2.5), and anything host-relative.

### 3.3 Four real entries

Written from the surface verified in §1. Comments are illustrative, not part of
the format.

```toml
# ── The DSN. Today: [database] path | url → SetSkyDefault("DB_PATH", …)
#    (build.rs:945-948), with the driver derived from the DSN's shape.
[[entry]]
key         = "data.db.url"
type        = "dsn"
scope       = "runtime"
tier        = "both"
summary     = "Connection string for Std.Db. Its shape selects the engine: postgres:// or a libpq keyword DSN opens PostgreSQL, anything else opens SQLite."
since       = "0.11.0"
env.suffix     = "DB_PATH"
env.prefixable = true
env.aliases    = ["DATABASE_URL"]
replaces       = ["database.path", "database.url"]

# ── Today: [live] store → SetSkyDefault("LIVE_STORE", …) (build.rs:1012),
#    read by chooseStore. `memory` is single-instance only (AGENTS.md
#    production gate).
[[entry]]
key         = "data.sessions.store"
type        = "enum"
values      = ["memory", "sqlite", "postgres", "redis"]
default     = "memory"
scope       = "runtime"
tier        = "both"
summary     = "Where Sky.Live keeps session state. `memory` and `sqlite` are single-instance; more than one replica requires `postgres` or `redis`."
since       = "0.9.0"
env.suffix     = "LIVE_STORE"
env.prefixable = true
requiredWhen   = { key = "deploy.replicas", greaterThan = 1 }
replaces       = ["live.store"]

# ── Today: NOT PARSED AT ALL. runtime-go/rt/observability.go:295 and :336
#    document the compiler emitting this; is_runtime_config_section
#    (build.rs:1073) does not list [security], so it is dropped in silence.
#    prefixable = false is the whole point: productionFromEnv reads the
#    literal ENV / SKY_ENV, and a generated reader makes that a fact of the
#    schema rather than a fact of one Go function.
[[entry]]
key         = "security.mode"
type        = "enum"
values      = ["dev", "production"]
default     = "dev"
scope       = "runtime"
tier        = "both"
summary     = "Production gate. `production` locks the dev console and banner, requires an auth secret, and turns on the metrics auth gate."
since       = "0.20.3"
env.suffix     = "ENV"
env.prefixable = false
env.aliases    = ["SKY_ENV"]

# ── Today: [database] embedded, matched-and-ignored at build.rs:989 with a
#    comment explaining that emitting it would leak the tier into the binary.
#    `scope = "toolchain"` is that comment, made machine-checkable: a
#    toolchain entry has no env suffix, so it CANNOT be emitted.
[[entry]]
key         = "toolchain.postgres.embedded"
type        = "bool"
default     = false
scope       = "toolchain"
tier        = "prototype"
summary     = "Let `sky run` supervise a per-project PostgreSQL cluster and inject its DSN. The app never learns which tier provisioned its DSN."
since       = "0.20.0"
replaces       = ["database.embedded"]
```

### 3.4 How the schema expresses the hard cases

**Compile-time vs runtime.** `scope` is the discriminator, and it is
*load-bearing rather than descriptive*: `scope = "toolchain"` forbids an
`env.suffix`, so the generator cannot emit a `SetSkyDefault` for it, so the
"the binary must never learn which tier provisioned its DSN" rule at
`build.rs:978-995` stops being a comment two people have to remember.

There is no `scope = "both"`, and that is deliberate. What looks like "both" is
one runtime setting with a compile-time *seed*: `SetSkyDefault` is set-if-unset
(`env_prefix.go:107`), so the `sky.toml` value is a default and the environment
always wins. Precedence is therefore a property of the mechanism, not of the
entry, and does not need per-entry declaration — which is why the schema has no
`precedence` field. The one thing that does vary per entry is the *fallback
chain*, and that is `env.aliases`.

**Prefixable names.** `env.prefixable` makes the name a function of `[env]
prefix`, resolved by the generated reader rather than by each call site. The
generated Go accessor is:

```go
// generated
func cfgDataSessionsStore() string {
    if v, ok := skyLookupEnv("LIVE_STORE"); ok { return v }   // prefixable
    return "memory"
}
func cfgSecurityMode() string {
    if v, ok := os.LookupEnv("ENV"); ok { return v }          // prefixable = false
    if v, ok := os.LookupEnv("SKY_ENV"); ok { return v }      // alias
    return "dev"
}
```

Two properties follow. First, §1.6's class becomes unrepresentable: a prefixable
entry cannot be read literally, because no hand-written call site reads it at
all. Second, the accessors are *functions*, evaluated on call — which retires
the `envPrefixHooks` re-init mechanism (`env_prefix.go:61-71`, `:116-118`) whose
entire job is to repair package-level vars that captured an env value before
`SetEnvPrefix` ran.

What `sky config` prints for a prefixable name is the **resolved** name, with the
template shown once:

```
data.sessions.store      postgres      SKY_LIVE_STORE
                                       └ prefixable: <PREFIX>_LIVE_STORE, PREFIX = SKY (default)
security.mode            production    ENV, SKY_ENV
                                       └ not prefixable — read literally by the production gate
```

**One suffix, one entry.** The generator rejects two entries sharing an
`env.suffix`, and rejects an entry whose suffix is read both prefixably and
literally. That single constraint closes three verified defects at once: the
`ADMIN_TOKEN` / `SKY_ADMIN_TOKEN` and `LIVE_BASE_PATH` / `SKY_LIVE_BASE_PATH`
splits (§1.6), the `ANALYTICS_SYNCHRONOUS_COMMIT` / `SKY_TELEMETRY_SYNCHRONOUS_COMMIT`
namespace drift, and `LIVE_TTL` — one environment variable that means a 30-minute
session lifetime at `live.go:4013` and a 30-day CSRF cookie window at
`csrf_middleware.go:83`. Two meanings, two defaults, one name, and an operator
who sets it gets both.

**Determinism.** The generator emits entries in schema-file order, which is a
committed file, so both outputs are byte-stable by construction — no map
iteration reaches emitted text (the adversary `repro_gate.rs:8-12` names). The
one behavioural change is that `SetSkyDefault` emission moves from *user file
order* to *schema order* (§1.7), which makes emission independent of how the
user arranged their manifest. That is strictly more stable and is a one-time
re-bless of the goldens (§7, stage 4).

---

## 4. Generation targets

| Output | Contents | Replaces |
|---|---|---|
| `rust/crates/config/src/generated.rs` | `pub static ENTRIES: &[Entry]` — every field of §3.2 as a `const` table | `is_runtime_config_section` (`build.rs:1073`), `accepted_config_keys` (`build.rs:1082`), and the `match` arms at `build.rs:934-1062` |
| `runtime-go/rt/config_generated.go` | `[]ConfigEntry` + one accessor per `scope = "runtime"` entry | the four hardcoded fallbacks at `lower.rs:822-825`; every hand-written `skyGetenv("…")` default; the `envPrefixHooks` re-init path |
| `docs/sky-toml.md` (generated region) | the at-a-glance table, the per-section key tables, the env-name column | the hand-written tables now drifting per §1.9 |
| `templates/sky.toml` + the `sky init` string | a scaffold emitted per `tier`, no commented-out duplicate sections | `main.rs:1220-1235` (§1.8) |
| `docs/coverage/config-ledger.json` | every entry, its reader site, its doc line — the reconciliation evidence | nothing; new |

The hand-written **`rust/crates/config/src/lib.rs`** is not generated. It holds:
the `toml_edit`-backed document reader, the typed accessors over `ENTRIES`,
provenance tracking, the validators that need cross-key logic
(`db_driver_conflict` moves here from `build.rs:894`), and the format-preserving
writer that replaces `pinned_sky_toml` (`db_provision.rs:166`).

### 4.1 Which of the seven scalar parsers dies

All seven, plus five of the seven structural scanners.

| Parser | Fate |
|---|---|
| A1 `read_sky_toml_config` `build.rs:911` | **deleted** — the generated table drives one reader |
| A2 `sky_toml_project_key` `build.rs:1164` | **deleted** — `project.bin` / `project.root` become entries; its path sanitisation (`build.rs:1197`) becomes `type = "path"` + a `segment` constraint |
| A3 `sky_toml_section_key` `build.rs:1221` | **deleted** — callers (`db_provision.rs:158`, `db_cluster.rs:1585/1658/1662`, `sky_toml_flag`) take typed accessors |
| A4 `parse_toml_entry` `main.rs:660` | **deleted** — `project.entry` |
| A5 `toml_entry` `main.rs:4472` | **deleted** — same key, one reader; the §1.2 scoping divergence between A4 and A5 disappears |
| A6 `db_driver_label` `main.rs:2447` | **deleted** — the inline-comment bug dies with it, and the prompt reads the same value the build does |
| A7 `toml_value` + `parse_toml_scalar` `db_pool_sizing.rs:168,191` | **deleted** — §1.3's divergence dies; the pool arithmetic stays, fed by the shared reader |
| B `pinned_sky_toml` `db_provision.rs:166` | **deleted** — `toml_edit` write |
| B `read_sky_toml` `coverage_ledger.rs:1124` | **deleted** — reads the shared document |
| B `declares_real_deps` `build_run_gate.rs:735` | **deleted** — reads the shared document |
| B `is_sky_package_root` `ffi_ops.rs:1164` | **deleted** — section presence from the shared document |
| B `check_auth_secret`'s header grep `main.rs:3679` | **deleted** — becomes a `requiredWhen` check |
| B `read_dependencies` / `upsert_dependency` / `remove_dependency` `ffi_ops.rs:1187/1037/1110` | **rewritten, not schema-driven** — `[deps.go]` is an open map of user-chosen keys, not an enumerable entry set. The schema declares the *table* (`kind = "map"`), and `toml_edit` does the read/insert/remove. Three hand-rolled scanners become three calls. |

That last row is the honest boundary: a schema enumerates *known* keys. Sections
whose keys are user-chosen get shared *parsing* and shared *editing*, but no
per-key declaration.

---

## 5. A better section layout

### 5.1 The principle: group by decision, not by module

Today's sections are named after compiler subsystems — `[live]`, `[database]`,
`[jobs]`, `[analytics]`, `[log]`, `[auth]`, `[env]`. A user does not think in
subsystems. The evidence that they think in *tiers* is in the repo already:

- `AGENTS.md` opens the app-building interview with "Question 0 — what is this
  for? (the tier drives everything)", and its table maps a single answer
  (prototype/internal vs production) onto database, session store, auth,
  deployment, and observability *simultaneously*.
- `docs/skydb/embedded-postgres.md:33-38` names four tiers, and its governing
  sentence at `:30-31` is **"The app binary never knows which tier it is in."**
  The tiers are distinguished purely by *who provisions the DSN*:

  | Tier (`embedded-postgres.md:35-38`) | Who provisions |
  |---|---|
  | Development | `sky` supervises a local cluster and injects the DSN |
  | Production, single app | the app itself under `--embed`, or an operator-set DSN |
  | Production, several apps on one host | one shared cluster, a DSN issued per app |
  | Managed / hosted | the platform injects the DSN |

  These are orthogonal to AGENTS.md's interview tiers (prototype/internal vs
  production), and both are real: the interview tier says *what the app must
  survive*, the provisioning tier says *who supplies the connection string*. A
  manifest has to express the first and stay silent about the second.
- `sky init`'s own scaffold (`main.rs:1203-1216`) and `.env.example`
  (`main.rs:1294-1298`) both present production as *one Postgres URL wiring app
  data, sessions, analytics and telemetry together*.

So the tier decision changes `[database] path`, `[live] store`,
`[jobs] storePath`, `[analytics] dbPath` and `[security] env` **at once**, and
today those are five keys in five sections, one of which does not exist. That is
the legibility defect, stated precisely: *the file's structure is orthogonal to
the axis along which it is actually edited.*

### 5.2 The proposed grouping

```
[project]                 identity + build inputs        — changed once, by the developer
[deps] / [deps.go]        dependencies                   — changed by `sky add`
[serve]                   how the app listens and serves — developer
[data]                    where state lives              — THE TIER DECISION
  [data.db]               the application database
  [data.sessions]         Sky.Live session state
  [data.jobs]             Std.Jobs queue state
  [data.analytics]        analytics + telemetry writes
[security]                mode, auth, CSRF, console gate — operator
[telemetry]               logs, traces, retention        — operator
[toolchain]               env prefix, embedded PG pin    — the dev/ops boundary
```

Five defensible properties:

1. **One section per owner.** `[project]`/`[serve]` are the developer's,
   `[data]`/`[security]`/`[telemetry]` are the operator's, `[toolchain]` is the
   boundary. A production checklist reads three sections, contiguously.
2. **The tier decision is one subtree.** Moving from prototype to production is
   an edit to `[data]` and `[security]`, not a scavenger hunt.
3. **Inheritance is expressible.** `[data] url` sets the default DSN for every
   subsystem; each `[data.*]` overrides only if it differs. This makes the
   scaffold's prose promise ("ONE DATABASE_URL wires app data + sessions +
   analytics + telemetry") a structural fact.
4. **`[security]` finally exists**, which is where `mode` (§1.5), `csrf`,
   the auth surface and the console gate belong — currently spread across a
   section that isn't parsed, an inert `[auth]` block, and env-only names.
5. **Subsystem names disappear from the file.** `[live]` meant "Sky.Live", which
   is an implementation the user picked, not a thing they configure; its two
   genuinely user-facing keys are "what port" (`[serve]`) and "where sessions
   live" (`[data.sessions]`).

Naming: camelCase keys throughout (Sky is Elm-family; `maxOpenConns` already is),
**no bare top-level keys**, and **one spelling per key** — the
`storePath`/`store_path` and `dbPath`/`dbpath` dual acceptances at
`build.rs:1038` and `build.rs:999` become one canonical name plus a
`deprecated`-flagged alias the migrator rewrites.

### 5.3 Before / after — a tier-1 app

**Before** (what `sky init` writes today, `main.rs:1220-1235`, comments elided):

```toml
name    = "notes"
version = "0.1.0"
entry   = "src/Main.sky"
bin     = "app"

[source]
root = "src"

[live]
port  = 8000
store = "memory"

[database]
driver = "sqlite"
path   = "app.db"
```

**After**:

```toml
[project]
name  = "notes"
entry = "src/Main.sky"

[data]
url = "app.db"          # SQLite. Sessions, jobs and analytics share it.
```

Six of the eleven lines were defaults (`version`, `bin`, `root`, `port`,
`store = "memory"`), one (`driver`) was decorative (§1.8 item 4), and the two
that mattered are now adjacent. The scaffold stops teaching a section layout the
user must later unlearn.

### 5.4 Before / after — a production app

**Before** (the shape `sky init --production` plus `.env.example` produce
between them, `main.rs:1203-1216` and `:1289-1301`):

```toml
name = "shop"
entry = "src/Main.sky"
bin = "app"

[source]
root = "src"

[live]
port  = 8000
store = "postgres"

[database]
driver          = "postgres"
path            = ""            # actually from DATABASE_URL
maxOpenConns    = 20
connMaxLifetime = "30m"
isolation       = "repeatable read"

[analytics]
retention = "180d"

[jobs]
store      = "postgres"
store_path = ""                 # the snake_case spelling the runtime's error text names

[log]
format = "json"
level  = "info"

[auth]
driver     = "jwt"
cookieName = "sky_auth"

# The production gate itself is not expressible here: ENV=production
# lives only in .env, and [security] is not a section the compiler reads.
```

**After**:

```toml
[project]
name  = "shop"
entry = "src/Main.sky"

[serve]
port = 8000

[data]
url = "${DATABASE_URL}"          # one DSN; every subsystem below inherits it
[data.db]
maxOpenConns    = 20
connMaxLifetime = "30m"
isolation       = "repeatableRead"
[data.sessions]
store = "postgres"
[data.analytics]
retention = "180d"

[security]
mode = "production"              # locks the console, requires the auth secret
auth = "jwt"                     # secret from SKY_AUTH_TOKEN_SECRET, never from this file

[telemetry]
log = { format = "json", level = "info" }
```

Four things changed that are not cosmetic. `[jobs]` disappears because it
inherits the DSN, which retires the `storePath`/`store_path` spelling trap whose
cost is documented at `build.rs:1026-1036`. `[database] driver` disappears
because the DSN decides (`build.rs:936-938`). `security.mode` is *in the file*,
which it cannot be today. And `${DATABASE_URL}` names the operator hand-off
explicitly instead of leaving `path = ""` to mean "actually set elsewhere".

`${VAR}` interpolation is a schema-level feature, not a parser hack: it is
legal only where an entry declares `interpolatable = true`, so it cannot creep
into `project.entry` and make the build non-hermetic.

---

## 6. Discoverability — `sky config`

The user's third complaint. Treated as a feature, the target is: *a user who
suspects a setting exists can find it, and a user whose setting is not working
can be told exactly why, without reading source.*

### 6.1 The surface

```
sky config                       # grouped listing: key, effective value, source, tier
sky config list [--tier production] [--section data] [--all]
sky config get <key>             # one value, machine-readable (--json)
sky config explain <key>         # what it is, type, default, env name, since, related
sky config why <key>             # the resolution chain INCLUDING the losers
sky config check                 # validate; non-zero exit; feeds `sky doctor` + `sky verify`
sky config env [--tier production]   # env names for the active prefix, paste-ready
sky config set <key> <value>     # format-preserving write via toml_edit
sky config diff --tier production    # what the production gate still needs
sky config migrate               # rewrite an old manifest to the new layout
sky config --from ./sky-out/app  # ask a BUILT BINARY what it was compiled with
```

`sky config check` emits `sky doctor`'s existing `Finding { check, severity,
message, hint, fix }` shape (`main.rs:3473-3479`) so the two surfaces share
rendering and `--fix`, rather than growing a parallel diagnostic vocabulary.

### 6.2 `explain` — the answer to "is there a setting for this?"

```
$ sky config explain data.sessions.store

  data.sessions.store : enum                        [runtime]  since 0.9.0
  Where Sky.Live keeps session state.

  values     memory | sqlite | postgres | redis
  default    memory
  env        SKY_LIVE_STORE          (prefixable: <PREFIX>_LIVE_STORE)
  read by    runtime-go/rt/live_store.go  (chooseStore)
  tier       memory, sqlite → single instance only
             postgres, redis → required for more than one replica

  related    data.sessions.path, data.url, serve.stickySessions
  docs       docs/skylive/architecture.md#session-stores

  formerly   live.store   (renamed in 0.21.0 — `sky config migrate` rewrites it)
```

`read by` is not prose. It is the reconciliation gate's output (§2.5) rendered
back to the user — so the line is *evidence the setting is live*, and it cannot
be printed for a key nothing reads.

### 6.3 `why` — the part that is actually missing today

Every failure below is a real class from §1, not a hypothetical.

```
$ sky config why data.sessions.store

  effective  memory          ← DEFAULT

  Resolution order, best first:
    1. process env   SKY_LIVE_STORE          unset
    2. .env          SKY_LIVE_STORE          unset
    3. sky.toml      [data.sessions] store   NOT FOUND
    4. default                               memory          ← in effect

  BUT sky.toml line 14 declares:
      [live]
      store = "postgres"

  `[live]` is not a section this Sky reads (renamed to [data.sessions] in
  0.21.0). It is being ignored in silence.
      fix:  sky config migrate
```

Six diagnoses the command must produce, each mapped to a verified cause:

| Symptom | Cause | Verified at |
|---|---|---|
| key in an unknown section, no warning | `[security]` absent from `is_runtime_config_section` | `build.rs:1073`, §1.5 |
| key misspelled into inertness | `session_ttl` vs `tokenTtl` in two shipped examples | `build.rs:1124-1128` |
| key parsed but nothing reads it | the `[auth]` class | §1.4 / reconciliation gate |
| value corrupted by an inline comment | three divergent scalar rules | `db_pool_sizing.rs:191`, `main.rs:2467`, §1.3 |
| env var set but ignored | `[env] prefix` in play — you set `SKY_…`, the app reads `FENCE_…` | `env_prefix.go:80`, §1.6 |
| **edited `sky.toml`, nothing changed** | runtime values are baked at build time by `SetSkyDefault` | `lower.rs:819-821`, §1.7 |

The last one deserves its own output, because it is invisible today:

```
$ sky config why data.db.url

  effective  postgres://…/shop   ← sky.toml [data] url

  ⚠ This value is COMPILED IN. ./sky-out/app was built 3 days ago from a
    sky.toml whose [data] url was "app.db". The binary will keep using
    "app.db" until you rebuild.
        fix:  sky build src/Main.sky
        or:   set SKY_DB_PATH in the environment — env always wins.
```

That check is why the Go side gets a generated table and not just accessors: the
binary answers `--sky-config` with its own compiled-in entries and their values,
and `sky config --from ./sky-out/app` compares them against the source tree.
Nothing else can tell a deployed binary from its manifest.

### 6.4 `check` — and where it runs

`sky config check` validates: unknown sections and keys, type and enum
violations, deprecated spellings, `requiredWhen` obligations, the cross-key
predicates that stay hand-written (`db_driver_conflict`), and prefix/alias
coherence.

The `requiredWhen` set for `security.mode = "production"` is taken from
AGENTS.md's production gate, and one entry in it is a *negative*:
`SKY_CONSOLE_AUTH` (`token` | `app` | `off`), `SKY_CONSOLE_TOKEN` when auth is
`token`, `SKY_ADMIN_TOKEN` for the metrics bearer, and — above one replica — a
shared session store plus cross-instance pub/sub. **`SKY_AUTH_TOKEN_SECRET` is
not among them.** AGENTS.md says so explicitly ("this gate used to say it was"),
and §1.9 confirms no runtime code reads it; its single reader is the build-time
check at `main.rs:3692`. It is therefore a `scope = "toolchain"` entry and a
`sky doctor` finding, not a runtime requirement — the schema is where that
distinction stops being re-litigated.

It runs inside `sky check`, `sky build` and `sky verify` — as **warnings**, for
the reason `build.rs:1139-1141` already gives: a project may carry keys a newer
Sky honours, and failing a build over an inert key is worse than the key being
inert. It runs as an **error** under `sky config check --strict`, which is what
CI and `sky verify --production` call.

---

## 7. Migration

Independently shippable stages. Each is useful alone; none requires the next.
No flag day.

**Stage 0 — measure the blast radius (no code).** Parse every `sky.toml` in the
repo (56 `examples/`, `apps/*`, `sky-bundled/*`, `templates/`, the corpus
fixtures) with a real TOML parser and list the files that are not valid TOML,
and the keys not in the current accepted set. A strict parser is stricter than
the tolerant scans; this is the one thing that could ambush every later stage.

**Stage 1 — the reconciliation gate, before any refactor.** `xtask config-gate`:
every `skyGetenv`/`skyLookupEnv` suffix in `runtime-go/` must appear in the
schema, every `scope = "runtime"` entry must have a reader, every entry must
have a doc line, and no suffix may be claimed twice (§3.4). Land the schema as
*description only* — nothing generated, nothing deleted. This alone closes the
`[auth]` / `[jobs]` / `[live] input` / `[security]` / `[live.broker]` class,
turns §1.10's 25 + 46 into a maintained ledger rather than a one-off audit, and
catches the dual-namespace splits. Per `docs/tooling/gate-harness.md` the gate
declares a falsifying mutation (delete a `skyGetenv` call → red) and
`--verify-falsifiers` proves it.

**Stage 2 — one parser, same schema, same keys.** Add the `config` crate with
`toml_edit`; move the seven scalar readers and five structural scanners onto it.
**No key renames.** This is the correctness third of the ask, and it deletes the
§1.3 divergence and the `main.rs:2467` bug on its own. Verified by the existing
gates plus a differential test asserting the new reader agrees with A1 on every
manifest in the repo.

**Stage 3 — generate.** `xtask config-gen` writes `generated.rs`,
`config_generated.go`, the `docs/sky-toml.md` tables and the scaffold;
`--check` gates drift. Still no renames. `is_runtime_config_section` and
`accepted_config_keys` are deleted here, not before — they are the safety net
Stage 2 is checked against.

**Stage 4 — `sky config`, read-only.** `list` / `explain` / `get` / `why` /
`check`. Nothing about the manifest changes; the discoverability complaint is
answered for the *existing* layout. This is where the emission order moves from
file order to schema order (§3.4) and the `repro`/`golden`/`coerce-floor`
baselines are re-blessed in one commit.

**Stage 5 — the new layout, with a migrator.** Every old key gains a
`deprecated = { since, use }` entry, so:

- old manifests keep working, with one warning naming the new key;
- `sky config migrate` rewrites in place, format-preserving;
- `sky init` scaffolds the new layout;
- `sky config explain` prints `formerly:` for a year.

Because deprecation is a schema field, the warning text, the migration mapping
and the docs note are one fact. Breaking changes are authorised at 0.20+, but a
mechanical migrator costs one field and removes the only reason not to.

**Stage 6 — `set` / `env` / `diff --tier` / `--from <binary>`.** The write path
and the deployed-binary introspection. Last because they are additive and
because `--from` needs the Stage 3 Go table.

---

## 8. What this does not fix

**A schema cannot prove a value is honoured, only that a reader exists.** The
Stage-1 gate checks that a suffix is passed to `skyGetenv` somewhere. It cannot
check that the result is *used*, or used correctly. `[live] input` is the
cautionary case: the runtime hardcoded `"debounce"` behind a `// or "blur"`
comment while two examples carried the key, "so the setting existed on both
sides and connected in neither" (`build.rs:1015-1018`). It is wired now
(`live.go:4781`) — but a variant where the value is read and then discarded
passes the gate. Closing that needs a behavioural test per setting, which is
Layer 2 (`apps/manifest.toml`) work, not schema work.

**Cross-key semantics stay hand-written.** `db_driver_conflict` (`build.rs:894`)
and `driver_for_dsn` (`build.rs:860`) mirror `rt.detectDriver`
(`runtime-go/rt/db_auth.go`) by hand, and that mirror is *itself* a two-copy
drift risk the schema does not touch. Same for the pool arithmetic in
`db_pool_sizing.rs`. Moving them into the `config` crate makes them findable;
it does not make them generated.

**Host-relative validity is out of scope.** Whether `maxOpenConns = 20` is
correct depends on the server's `max_connections` and how many replicas exist.
`sky config check` can warn from `docs/skydb/embedded-postgres.md`'s arithmetic;
it cannot know the host.

**Provenance is best-effort in a deployed process.** `SetSkyDefault` writes into
the real environment (`env_prefix.go:107-108`), so after `init()` a `sky.toml`
default and an operator `export` are indistinguishable. The generated table can
record what it seeded, giving `--from <binary>` an honest answer, but a value
injected by a wrapper script before the process starts is just "environment" and
always will be.

**Secrets are marked, not protected.** `secret = true` gets redaction in every
listing. It does nothing about a secret reaching a log by another route.

**Runtime-mutated config is invisible.** `System.setenv` (documented at
`docs/sky-toml.md:565-568`) changes values after `init()`. `sky config` reports
the startup picture.

### Risks

1. **The strict-parser ambush.** A real TOML parser will reject manifests the
   tolerant scans accept. Stage 0 exists solely to find out how many, before
   anything depends on the answer. If the number is large, Stage 2 needs a
   lenient-with-warning mode first — which is a parser fork, i.e. the thing this
   design exists to abolish. This is the single largest schedule risk.
2. **The drift gate could be vacuous.** This repo has a documented class of
   gates that recorded PASS while never running. `config-gen --check` and
   `config-gate` must ship with declared falsifying mutations and be proven red
   under `xtask harness --verify-falsifiers` in the same commit.
3. **Byte-stability re-bless.** Stage 4 changes emitted `SetSkyDefault` ordering
   for any project whose manifest is not already in schema order. `repro`,
   `golden` and `coerce-floor` baselines move together, in one commit, or the
   next contributor inherits an unexplained red.
4. **Dependency weight.** `toml_edit` pulls `winnow` and friends into the
   compiler's build. Measure before committing; `basic-toml` plus a hand-written
   writer is the fallback, and it reintroduces exactly the read/write split that
   produced §1.3 — so it is a fallback, not an alternative.
5. **Renaming touches everything.** Stage 5 rewrites 56 examples, `apps/*`,
   `sky-bundled/*`, templates, corpus fixtures and every doc snippet.
   `sky config migrate` must be run over the repo by the same commit that lands
   the deprecations, and `scripts/doc-examples.sh` must be green after it.
6. **The schema becomes a second place to lie.** An entry can claim
   `since = "0.9.0"` or a `summary` that no longer matches behaviour. The gate
   checks names and readers; it cannot check prose. That is a smaller surface
   than nine hand-maintained copies, but it is not zero.

---

## Appendix — verified / inferred

**Verified by reading the cited line on `0fdee2c4`:** §1.1 (workspace deps,
lockfile), §1.2 (all fourteen sites), §1.3 (both `parse_toml_scalar`
implementations and `main.rs:2467`), §1.4 (`build.rs:1073/1082/1142` and the
example story in its doc comment), §1.5 (absence of `security` in
`rust/crates/**` and live `docs/`; the four runtime comments), §1.6
(`env_prefix.go` in full; `isProd`, `productionFromEnv`, `isProductionMode` and
their call sites; the three dual-namespace pairs), §1.7 (`env_prefix.go:107`,
`dotenv.go:47-64,96,105,139,162,167`, `lower.rs:795-827`), §1.8
(`main.rs:1196-1235`), §1.9 (the four searches proving no `AUTH_*` reader;
`startup_report_test.go:110-111`), §1.10 (counts recomputed by grep over live
`docs/**` and non-test readers, with the suffix form included), §1.11
(`docs/sky-toml.md` at the cited lines; `docs/skyauth/overview.md:198`;
`docs/skylive/pubsub-design.md:1109`), §5.1 (`embedded-postgres.md:30-38`,
AGENTS.md "Question 0" and "Production gate").

**Inferred, not executed:** the `sky db reset`/`drop` prompt mislabelling
(§1.3) — read from `db_driver_label` and its caller at `main.rs:2538`; the
Secure-cookie / panic-log consequence of the `isProd` split (§1.6) — read from
`rt.go:10209` and `:10233`, no deployment reproduced. Nothing here was executed,
in line with the read-only, no-heavy-build constraint this document was written
under.

**Corrected from the earlier inventory:** its "seven parsers" is right for
scalar readers and undercounts the total (fourteen, §1.2); its "31 documented
names with no reader" recomputes to **25** and its "48 read names in no live
doc" to **46** (§1.10); and its claim that the DB pool knobs are undocumented is
**false** — they are documented and read, and the audit was misled by the
docs writing `<PREFIX>_…` where the runtime warns with `SKY_…` (§1.10). That
last one is the most useful correction in this document: an audit of the config
surface was itself defeated by the config surface's discoverability.
