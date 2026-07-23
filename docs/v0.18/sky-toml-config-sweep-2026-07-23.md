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

### Still open (documented-but-ignored — tracked, not yet closed)

- **`bin` (output binary name).** docs/sky-toml.md documents `bin = "app"` →
  `sky-out/<bin>`, but the output name is hardcoded `app` across ~7 build/run
  sites (build.rs:505/608, driver.rs:149, main.rs:436/1467/2225/2634). Closing
  it means threading a `bin_name` through `BuildOptions` + the `go build -o`
  arg + every `Command::new("./app")` run path + the completion message.
  Low real-world impact (everyone ships `sky-out/app`); needs a careful
  single-pass thread so no run site is missed.
- **`[source] root`.** docs documents `root = "src"` for module resolution, but
  discovery hardcodes `example_dir.join("src")` (build.rs:146, :1026). Reading
  the root requires parsing sky.toml BEFORE discovery (config is currently read
  after). Rare in practice.
- **`[auth]` runtime consumer.** `cookieName`/`tokenTtl`/`driver` now seed
  `AUTH_*` env defaults, but the runtime auth layer does not yet READ
  `AUTH_COOKIE`/`AUTH_TOKEN_TTL`/`AUTH_DRIVER` at every relevant site — the
  seed is a no-op until each `Std.Auth` read routes through `skyGetenv`. Deeper
  runtime work; the env-seed half is done.
