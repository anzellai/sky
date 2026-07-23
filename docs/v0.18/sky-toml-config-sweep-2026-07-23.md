# sky.toml config-honouring sweep (2026-07-23, user-reported)

The user found `[live] static` (and session-store) not honoured — the runtime
fell back to `SKY_*` env vars. A sweep of EVERY documented sky.toml key against
`read_sky_toml_config` (build.rs) found the reader only handled `port` +
`[database]`; every other runtime key was silently dropped.

## Root causes

1. **Missing keys.** `read_sky_toml_config` didn't read `[live]` (static / store
   / storePath / ttl / maxBodyBytes), `[auth]` (cookieName / tokenTtl / driver),
   `[log]` (format / level), or `[env] prefix`. Each is now mapped to the exact
   suffix the runtime reads (`store` → `LIVE_STORE` via `chooseStore`; `static`
   → `LIVE_STATIC_DIR`; `format` → `LOG_FORMAT`; …) and emitted as
   `rt.SetSkyDefault`.
2. **Ordering bug.** `prologue_init` emitted the FIXED fallbacks (`LIVE_TTL=1800`,
   `AUTH_*`) BEFORE `extra_defaults`, and `SetSkyDefault` is set-if-unset — so the
   fixed default always clobbered the sky.toml value (`[live] ttl` was a no-op).
   Now sky.toml values emit first (win); fixed fallbacks emit after (no-op when
   overridden). Env still wins over both.
3. **`[env] prefix` emitted NOTHING.** docs/sky-toml.md documents the compiler
   emitting `rt.SetEnvPrefix(...)`, but the Rust compiler didn't — a custom env
   namespace was silently ignored. Now emitted as the FIRST init() statement (so
   every default seeds under the custom prefix). `LowerConfig` gains `env_prefix`.

## Precedence (unchanged contract)

`shell env / .env  >  sky.toml  >  fixed default`. `SetSkyDefault` only sets the
env var when unset, so a real env override always wins.

## Verified

`sky_toml_tests::live_and_auth_keys_map_to_runtime_default_suffixes` (all of
[env]/[live]/[auth]/[log]/[database]); emitted init() order confirmed
(SetEnvPrefix first → sky.toml defaults → fixed fallbacks); repro PASS
(byte-stable); golden PASS; workspace + examples green.

## Doc alignment

templates/CLAUDE.md + CLAUDE.md used `[auth] cookie`/`ttl`; corrected to the
canonical `cookieName`/`tokenTtl`/`driver` (docs/sky-toml.md + the build reader).

## Follow-up (2026-07-23, session cont.)

Closed since the first pass:

4. **`[log]` runtime re-sync.** The `[log] format`/`level` defaults were
   emitted via `SetSkyDefault` but the runtime still logged plain — rt's
   `logJSON`/`logThreshold` package vars are evaluated at `rt` package-init,
   BEFORE the app's generated `init()` runs. Fix: `SetSkyDefault` now re-fires
   the `envPrefixHooks` (same mechanism `SetEnvPrefix` uses), so the cached log
   state re-reads env. Regression `TestSetSkyDefaultResyncsLogConfig`.
5. **`[database] url` alias.** CLAUDE.md's app-matrix uses `url = "…"` but the
   reader only recognised `path`. Both now seed `DB_PATH` (detectDriver routes
   a `postgres://` DSN to pgx). Regression `database_url_is_an_alias_for_path`;
   docs/sky-toml.md updated.

### Closed (2026-07-23, session cont.)

- **`bin` (output binary name)** — CLOSED. A shared `configured_bin_name`
  reader (sky.toml `bin`, default `app`, sanitised to a single path segment so
  `go build -o` can't escape the out dir) is wired into the `go build -o` arg
  AND every run path (`build_example` run, `driver::run_app`, `sky watch`
  restart, `sky verify`, the completion message). `main.rs:2634` was a red
  herring — it reads the oracle's `legacy-haskell-compiler/app/VERSION`, not the
  built binary. `sky-out/<bin>` verified e2e; default stays `sky-out/app`.
- **`[source] root`** — CLOSED. `configured_source_root` (sky.toml `root`,
  default `src`, also accepts the `[source]` table form) drives module
  discovery (build.rs), so a project with sources under `lib/` builds.
- **`[auth]` runtime consumer** — RESOLVED as a docs clarification. `Std.Auth`
  is a library: `signToken secret claims expirySeconds` takes the secret + TTL
  as ARGUMENTS and the cookie is set by the handler, so there is no framework
  layer to "consume" the keys. The `[auth]` keys correctly seed `SKY_AUTH_*`
  env vars that USER CODE reads via `System.getenvOr`. docs/sky-toml.md
  corrected to describe this accurately.
