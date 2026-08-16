# Embedded PostgreSQL — local end-to-end delivery proof (2026-08-16)

**Claim proven:** `sky db provision --embed` → `sky build --embed` →
`SKY_DB_OP=migrate ./app --embed` → `./app --embed` (serving real traffic) runs
end to end against a **locally built** darwin-arm64 bundle, with every
assertion below taken from the running system, not from build logs.

**Harness:** `scripts/skydb/e2e-local-delivery.sh` (committed with this
document; it reproduces everything below from a built bundle in one
invocation). Run on macOS 26.5.2 / arm64, branch `feat/embedded-postgres`,
base `82a5add2`.

## Why a local bundle proves the delivery path

No `postgres-bundle-v*` release exists, and the only trigger of
`.github/workflows/postgres-bundle.yml` is a tag push. But
`db_provision::base_url` treats `SKY_POSTGRES_BUNDLE_URL` as the release
directory, and everything after URL construction — manifest-first fetch,
sha256 verification against `SHA256SUMS`, extraction, `bundle_runs`
install gate, the `go:embed` repack, runtime extraction, initdb, launch — is
byte-identical code either way. The release directory was simulated **exactly**
as the workflow publishes it: `tar -czf` of the `postgres-18.6-darwin-arm64`
directory, `sbom-darwin-arm64.json` beside it, `sha256sum -- *.tar.gz *.json >
SHA256SUMS`.

## What ran, in order

1. **Bundle build** — `scripts/skydb/build-postgres-bundle.sh --jobs 4`
   (PostgreSQL 18.6 + pgvector 0.8.6 + pg_partman 5.5.0, pinned checksums),
   ~12 min, exit 0. The script's own completeness + relocation smoke gates
   passed — including under this host's nix cctools
   `install_name_tool`/`otool` with Apple `/usr/bin/codesign`, the
   combination that had never been tested.
2. **Licence gate on the real artefact** —
   `scripts/skydb/test-licence-gate.sh --bundle <bundle>`: **15/15** after the
   fix below, including **C8** (real bundle accepted) and **C9** (same bundle
   with a GPL-linked extension planted in `lib/` rejected as
   `COPYLEFT UNVENDORED`, citing readline). The gate discriminates on the
   shipping artefact, not only on fixtures.
3. **Provision** — `SKY_POSTGRES_BUNDLE_URL=file://<release> sky db provision
   --embed`: manifest-verified fetch, extraction into `$SKY_HOME/postgres/18.6/`,
   `[database] postgresVersion = "18.6"` pinned into `sky.toml`.
   `$SKY_HOME/postgres/18.6/bin/postgres --version` → `postgres (PostgreSQL) 18.6`.
4. **Migrations** — `sky db init` + `sky db migrate --gen init` against a
   `db : Store.Project` (`Std.Db.Schema.toProject`) in the entry module wrote
   `db/migrations/1786899940_init.json` (one `createTable`).
5. **Build** — `sky build --embed src/Main.sky`: 122 MB `sky-out/app`
   carrying `sky-out/postgres-bundle.tar.gz` (25 MB) via generated
   `sky-out/pg_embed_bundle_gen.go`, plus `embedded_migrations.go`.
6. **One-shot migrate** — `SKY_DB_OP=migrate ./sky-out/app --embed --data-dir
   <durable>`: extracted the bundle, ran initdb, started 18.6, applied the
   migration (`db: applied 1 migration(s): 1786899940_init`), exited.
7. **Serve** — `./sky-out/app --embed --data-dir <durable>`, then over HTTP:
   - `/version` → `PostgreSQL 18.6 on aarch64-apple-darwin25.5.0, compiled by
     clang version 21.1.7, 64-bit`
   - `/notes` → `notes=1`, then `notes=2` (real writes through the migrated
     table)
   - restart → `/notes` → `notes=3` (data survived; no re-initdb, no
     re-extraction — the `.sky-bundle` content-digest marker matched)
   - app exit left no postgres process behind.

### The PATH-fallback falsification

This host has Homebrew **PostgreSQL 14.21** first on `PATH`, and the runtime's
binary discovery falls through to `PATH` — an app that embedded nothing would
still start a cluster and look green. The embedded branch is proven by:

- `<data>/runtime/.sky-bundle` exists and records
  `postgres-bundle.tar.gz sha256:914ab63d…` (the marker only the embedded
  extraction path writes), and
- the **served** `select version()` says **18.6**, not 14.21.

## Findings a first public cut would have hit

1. **The licence gate rejected the shipping artefact** (now fixed).
   `build-postgres-bundle.sh` ships six binaries — `SHIPPED_BINARIES=(postgres
   initdb pg_ctl pg_dump pg_dumpall pg_restore)`, with `pg_dumpall` added
   deliberately (roles are cluster-wide; a role-less `pg_dump` is not a
   restorable backup) — but `scan-bundle-licences.sh`'s classification table
   knew only five. First-ever run against a real bundle: `UNKNOWN shipped
   object bin/pg_dumpall` → `GATE FAIL`, C8 red, and C9 red for the wrong
   cause. In CI this fires at the workflow's scan step, **before** upload —
   the tag would have produced no release, with a re-run only possible by
   re-tagging. Fixed in this commit by classifying `pg_dumpall` (same source
   tree, same PostgreSQL Licence); the full fixture suite plus C8/C9 then
   passes 15/15. The fail-closed design worked exactly as documented — the
   gate's first contact with a real artefact is precisely when the UNKNOWN arm
   earns its keep.
2. **`--embed` refuses a `/tmp` data directory — by design, and the docs'
   example commands must not use one.** `rt.rejectTempDataDir` refuses
   `--data-dir` under `/tmp`, `/var/folders`, `$TMPDIR`, etc., because under
   `--embed` that directory holds the app's only copy of its data. Correct
   behaviour — but it means every runbook/example that reaches for
   `--data-dir /tmp/...` (as this proof's own first attempt did) fails with
   the (excellent) refusal message. Durable location required; the harness
   defaults to `$HOME/.sky-e2e-embed-proof/data`.
3. **Toolchain hazards that did not fire:** the pinned-checksum fetch, the nix
   pkg-config wrapper (script pins `/opt/homebrew/bin/pkg-config`), and the
   nix-cctools + Apple-codesign re-signing combination all behaved; the
   bundle's six binaries run from the relocated tree.

## What this does NOT prove

- The **GitHub release URL** and its redirect: no test has resolved
  `https://github.com/…/releases/download/postgres-bundle-v18.6/…` against
  github.com, because the release does not exist. `curl -L` is present in both
  fetch sites, but the hop count, asset naming under `softprops`, and the
  redirect behaviour are unexercised.
- The **other three platforms** (linux-amd64, linux-arm64, darwin-amd64):
  this Mac cannot build them; their relocation arms (`patchelf`,
  `$ORIGIN/../lib`) have never run against a real bundle.
- The workflow file itself (matrix, artifact plumbing, release assembly) —
  only its archive/manifest **format** was reproduced, not its execution.
