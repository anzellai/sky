# Release defect tracker — discovered from real-project use (2026-07)

General fixes only — nothing hard-coded for any specific app. Every defect has a
regression test (item 10). All 11 done.

| # | Item | Status |
|---|------|--------|
| 1 | `sky doc` / `--list` include all stdlib (Std.Live + kernel-only modules) | ✅ `5e768a5d` |
| 2 | Std.Ui documented as the clear default for app interfaces | ✅ `74efc069` |
| 3 | `Std.Auth.login` type ↔ doc ↔ runtime agree (returns user id Int) | ✅ `bd3fe389` |
| 4 | `main` collision — `<main>` is `Html.mainNode`, not `main` | ✅ `dacb13bf` |
| 5 | `sky test` respects custom `bin` names | ✅ `c186af0b` |
| 6 | `sky init --help` shows help, doesn't create a project | ✅ `c186af0b` |
| 7 | Install in unprivileged environments (unwritable $HOME) | ✅ `c186af0b` |
| 8 | Compact `CLAUDE.md` template (~150–200 lines) → 191 lines | ✅ `74efc069` |
| 9 | Complete API signatures in `sky doc`, not `CLAUDE.md` | ✅ `74efc069` |
| 10 | Regression test for every defect | ✅ woven per-item |
| 11 | `sky verify` = fmt + type-check + build + tests | ✅ `3317c9cd` |

## Regression tests added
- #1 `kernel_only_module_is_queryable` (project/doc)
- #3 `TestAuth_Login_ReturnsUserIdMatchingRegister` (runtime-go/rt)
- #4 `html_main_landmark_is_mainnode_no_entrypoint_collision` (project)
- #5 `respects_custom_bin_name` (testrunner, real build+run)
- #6 `wants_help_detects_help_flags_only`, `profile_flags_stripped_from_args` (sky)
- #7 `is_writable_dir_probes_correctly`, `env_unset_or_empty_treats_empty_as_unset` (ffi)
- #11 `verify_walk_sky_skips_generated_dirs`, `verify_single_project_routing` (sky)

## Sibling deliverables (same session)
- `sky run --profile` — runtime profiling with a plain-language, Sky-named REPORT.md.
- #164 follow-up (union vs same-named imported alias).
