# Autonomous mandate: v0.29.0 real-world capabilities, tested and shipped

Started 2026-10-28. Branch `feat/realworld-requests` (worktree `../sky-realworld`).
Private context (request files, app list): `.claude/AUTONOMOUS_GOAL.private.md` (gitignored).
The previous mandate (Std.Ai lifecycle) is complete: v0.28.0 shipped, then v0.28.1.

## The user's goal, verbatim

> we also need some improvements + new stdlibs to cover realworld use cases

> please for the remaining items, all in single tag release.

> ok please manage e2e + testing + validation, 100% fully unattended + autonomous + PIV
> until 0.29.0 is fully tested + shipped
>
> migrate all my private apps + then redeploy, except [the app named in the private companion]

> for the v0.29.x release, i think all the docs, readme.md need a refresh, many contents are outdated now.
> we need to also keep sky-lang.org and its doc sites all up to date with stdlib modules, Std.App single entry point.
> Std.Ai new modules training etc...
>
> BUT also, try to make readme.md a bit more simpler as well, now we're getting more mature in releases, i guess some older context: TS/HS compiler can be hidden -- or even removed from latest tag source?
>
> focus on bringing what sky CAN achieve, fitting for what workflow + design principles (why it exists and what problems it solves).
> and a few simple "WOW" factor sample code?
>
> i don't know, do some industrial deep dive analysis how to best promote and convert visitors to install sky + try it.
>
> apparently visitors are either interested in:
> - Elm itself, or FP languages
> - Go perhaps even, zero FFI code!
> - simplicity, scalable fullstack programming language, dislike npm/java deps/python slowness + exception handling broken production apps... etc.
> - AI native, AI driven workflow + single compiled binary portable + scalable with plenty of realworld examples
>
> we need to SELL sky really well in first 10-30s reading our readme.md
>
> that should go together in v0.29

> essentially the repo needs some tidying up, keeping only the relevant + required files/scripts for current compiler.
> and ensure everything is up to date, including nix setup, script, versions, docs etc.
> so i expect v0.29 will have a very clean repo + folder + files structure going forward.

> also for the claude.md + agents.md whatever related/relevant AI tooling files, we need to do some refresh/update,
> keep context as much as possible -- but may clean up unnecessary parts/outdated parts or files

## Scope (user decisions, 2026-10-28)

- ONE tag, v0.29.0, from this branch, when the whole batch is green on one SHA in
  both the Rust CI and the release workflow. No partial tags.
- Items: sky run args; let mutual recursion; core functions (List.map2/map3 typed,
  List.unique, String.indexes, Char.isAlphaNum/isSpace); Regex Match API; Process
  withStdin + runCommand; Native.selection/setSelection; collection performance
  (cons, Dict D1 + D2 B+-tree with one accepted session reset, Array, String,
  perf-gate, cost lines); Regex option A (`type Regex`, `Regex.fromString` the only
  constructor, String forms removed, literal patterns checked at compile time,
  `"""\s+"""` tested); value-cycle rejection; Server.header case-insensitive; Spa
  cookie Max-Age; split alias shadowing; persist-exclude API; configurable go build
  limit; Std.Xml (+ Decode/Encode), Std.Zip, Codec.toXml/fromXml; Std.Office.Xlsx and
  Std.Office.Docx (read and write); Std.Image.Canvas with WebpLossless (lossless,
  pure Go); animated GIF in Std.Image (Gif format, Preserve keeps GIF, per composed
  frame, limits, Image.frames, Image.reduceFrames) plus a pixel limit for still images.
- Every breaking change has a migration section in docs/migration/v0.29.md.
- A bug found during the work ships in this release.
- Docs refresh in v0.29.0: every live doc checked against the current surface;
  README rewritten short, conversion-first (what Sky achieves, why it exists, a few
  "wow" samples, an install line in the first screen), guided by a written
  conversion analysis; the legacy Haskell / TypeScript / Sky-in-Sky compiler trees
  are removed from the tree (they stay in git history and older tags; CI does not
  build them); sky-lang.org and the docs site updated to the v0.29 stdlib (Std.App
  single entry point, Std.Ai training modules, the new modules of this release).
- Repo tidy in v0.29.0 (user, 2026-10-29): keep only the files and scripts the
  current Rust compiler, its runtime, stdlib, tests, CI, examples and docs need.
  Remove dead scripts, stale artefacts, legacy-only tooling and orphaned folders
  (git history and older tags keep them). Bring the nix setup, scripts, pinned
  versions (Rust, Go, Node, tools) and docs up to date. Every removal is checked
  against what CI, the release workflow and the gates reference, so nothing a gate
  needs is lost. The result is a clean folder structure going forward.
- After the tag: migrate every private app (list in the private companion) to
  v0.29.0 and redeploy, except the one named there as excluded.
