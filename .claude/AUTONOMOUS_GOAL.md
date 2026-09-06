# AUTONOMOUS MANDATE — Sky.Spa hardening + SSR + sky-lang.org SPA deploy (2026-09-06)

## Verbatim goal (user, 2026-09-06, going on a flight — away for hours)

> ok I'm going on a flight now so I will be away for hours.
>
> you MUst now take charge and ask no questions or permissions or continuity, in
> 100% fully unattended+autonomous+PIV mode to deliver the fixes, updated all
> contents accordingly and then manage e2e to deploy the SPA version of
> sky-lang.org accordingly.
>
> after that you can reply the GitHub issue don't close, and then continue working
> the v1 roadmaps.
>
> nothing is paused until ALL the above goals are fully achieved

## Ordered goals (all must be FULLY achieved; nothing pauses until then)

1. **Deliver the fixes → v0.23.1 release.** SPA compiler fixes (#195 + BUG-1
   App.webDefaults + BUG-2 silent-drop + BUG-3 diagnostic) are on main (f3f09279).
   Full release gate running in CI (per-commit Rust CI + dispatched nightly-sweep
   full tier). When GREEN → **TAG v0.23.1 + cut the GitHub release** (mandate =
   the authorization; the usual "confirm the tag" rule is OVERRIDDEN by this
   explicit "ask no permissions" directive). Stop at the gh release (no SkyDeploy
   redeploy, per standing pref).
2. **Update all contents accordingly.** sky-lang.org samples already migrated to
   Std.App (c9fecc1). As the SPA/SSR migration lands, update site content + docs
   so everything reflects the shipped reality (Std.App, Sky.Spa+SSR).
3. **Build SSR/prerender for Sky.Spa + migrate + deploy the SPA version of
   sky-lang.org e2e.** THE BIG ARC. Design delivered: docs/skyspa/ssr-design.md
   (branch design/skyspa-ssr). Phases P0 withHead→P1 chrome SSR+hydration→P2
   static-backend copy→P3 data-resolved SSR (the full SEO win content sites need)
   →P4 DX/gates. Then re-architect sky-lang.org (purify init/view, auth-via-RPC),
   build --target web:app WITH SSR, and DEPLOY it e2e to the sky-lang-org VM
   (deploy/deploy.sh; the site MUST keep SEO = data-resolved SSR is required).
   Verify live (crawlable HTML + hydration + all routes + auth/admin/DB via RPC).
4. **Reply to GitHub issue #195 — DO NOT CLOSE.** After v0.23.1 ships, post a
   reply telling the reporter the fix shipped in v0.23.1 (`sky upgrade`). Leave
   the issue OPEN.
5. **Continue the v1 roadmap.** Resume v1-readiness work (B1 coverage curation
   [[b1_stdlib_api_curation_scope]] + residuals; see prior v1 mandate history).

## Operating mode (user, 2026-09-06 — INVIOLABLE for this mandate)
100% FULLY UNATTENDED + AUTONOMOUS + PIV. Take charge; ask NO questions, NO
permissions, NO continuity checks. Tag/release, deploy, reply-to-issue are ALL
authorized by this mandate. On a genuine blocker I cannot self-serve (external
auth wall I have no path around), document it + CONTINUE other goals; never stall
the whole loop. Checkpoint (commit+push, gates green) at each phase boundary.

## PIV per phase (CLAUDE.md §0.3/§0.4)
architecture-consult (docs/skyspa/ssr-design.md is the SSR arch reference;
docs/rust-rewrite/ for lowering) → adversarial grill → implement → fresh-context
Judge at close. Use ISOLATED WORKTREES for concurrent code agents (the shared-tree
lesson from 2026-09-06 — two agents in one tree nearly collided). Narrow gates per
change; full gate at milestones only.

## Constraints (durable)
- No co-author trailer. Batch commits; push at phase boundaries. Root-cause fixes
  only; regression-test-first. Secrets typed; Error not String.
- sky-lang.org is on the sky-lang-org VM (settleby, us-central1-a, e2-small, static
  IP 34.10.201.196; SSH works when VM healthy) — see [[skylang_org_infra_incident]].
  Deploy via deploy/deploy.sh --project settleby. gcloud flags INLINE (zsh no-split).
- SEO is non-negotiable for sky-lang.org → the SPA deploy REQUIRES data-resolved
  SSR (P3). Do NOT deploy a no-SSR SPA that blanks the crawler.

## PROGRESS
- GOAL 1 ✅ **v0.23.1 RELEASED** — tagged at fcc24767, GitHub release live
  (https://github.com/anzellai/sky/releases/tag/v0.23.1). SPA fixes (#195 + BUG-1
  App.webDefaults + BUG-2/3) + Task.parallelN + Std.Db by-id. Gate: all real gates
  green (config-gates, test-sky/rest, example-sweep, behaviour-corpus, harness-t3,
  web-runtime); the falsifier CI red was a stale-compiler false-red (conformance
  proven 27/27 fresh); build-corpus-2 was a transient cancel (rerun green).
- #195 ✅ REPLIED (comment 5560622272) + left OPEN, per mandate (fix shipped in
  v0.23.1; done now since the fix is live, not deferred to after the SPA deploy).
- SSR: ✅ design grilled+revised (590b2edd) → P0 (withHead) + P1 (chrome SSR +
  hydration) IMPLEMENTED + MERGED to main (f5062b2c). Crawlable SSR PROVEN: a
  server-branch fixture built via --target web:app serves real body content +
  per-route <head> + data-sky-ssr (HTTP 200). Verifying on main + regenerating
  censuses (Spa.withHead new symbol) now (bg b128k1mj1).
- REMAINING: SSR P3 (per-route data-resolved SSR — the full SEO win for a routed
  content site; sky-lang.org needs it); then GOAL 2/3 = update sky-lang.org content
  + migrate init/view to the SSR-split shape + deploy --target web:app+SSR e2e +
  verify live; then GOAL 5 v1 roadmap.
- FOLLOW-UP (no-deferral, pre-existing, NOT v0.23.1 blockers): (a) kernel-members
  drift `List.sortWith` missing from KERNEL_FUNCTIONS[List] (shipped v0.23.0 via
  3d3a7776); (b) CI runs `kernel-members` WITHOUT `--check` (rust-ci.yml:849) so it
  never gates drift — wire `--check` in. Fix both alongside the SSR work.
- NEXT: land census regen + push main; implement SSR P3; then sky-lang.org SPA-SSR
  migrate+deploy; then v1.
