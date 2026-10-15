# Mobile shells: a rebuild runs no shell tool

`mobile:ios` and `mobile:android` builds of a scratch copy of
`examples/64-app-native`, before and after the change that builds a shell only
when one of its inputs changes (commit `0a796e090`, merged into
`perf/build-speed` at `d68e0d99d`).

- **Machine**: 16 GB Apple silicon Mac, Xcode in `/Applications`, Android SDK
  (build-tools 34.0.0, 35.0.0, 36.0.0-rc1; platforms 33 to 36), Homebrew
  openjdk 25, 2026-10-15.
- Build only. Nothing was installed or launched on a simulator or a device.

## Old behaviour against new, rest of the build warm (3 repeats)

"Forced" deletes the shell stamp before the build, so the shell tools run as
they did on every build before the change.

| Target | Forced (old: every build) | Skipped (new: unchanged shell) |
|---|---|---|
| `mobile:ios` | 2.58 / 2.56 / 2.64 s | 1.08 / 1.10 / 1.08 s |
| `mobile:android` | 3.36 / 3.20 / 3.09 s | 0.94 / 0.94 / 0.94 s |

Peak RSS (largest process, `/usr/bin/time -l`): forced iOS 324 to 330 MB,
forced Android 177 to 180 MB, skipped 133 to 135 MB. The skipped iOS app passes
`codesign -v`.

## First and second build (one run each)

| Run | Target | Wall | Shell tools run |
|---|---|---|---|
| first (wasm, backend and shell cold) | `mobile:ios` | 13.32 s | yes |
| second, identical | `mobile:ios` | 1.08 s | no |
| one Sky source edit | `mobile:ios` | 1.67 s | no |
| first (wasm warm, shell cold) | `mobile:android` | 3.88 s | yes |
| second, identical | `mobile:android` | 0.96 s | no |
| one Sky source edit | `mobile:android` | 1.68 s | no |

## Tests

`an_unchanged_ios_shell_runs_no_tool` and `an_unchanged_android_shell_runs_no_tool`
(PATH shims) failed with the skip disabled (2 tools run on iOS: swiftc and
codesign; 4 on Android: aapt2 twice, javac, d8) and pass with it.
