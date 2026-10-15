# AUTONOMOUS GOAL (captured verbatim 2026-10-15)

Branch: `perf/build-speed`. The earlier v0.27.0 mandate this file held is done
(released). This mandate replaces it.

> so for [a private project]/repo, the github action cicd takes 70mins+ to
> build web:app so i switch to build in GCP VM instance with a large instance
> 8-CPU VM, plenty of RAM.
>
> but it still takes >30mins to build, realising dispite the multi-cores, sky
> only build with single core?! and not leveragin what machine capabilities
> can provide and speed up?
>
> can you investigate and see anything we do with sky compiler (essentially go
> build right?) to massively speed up builds regardless for App.live or
> web:app or native apps?

> for medium sized project (which [the private project] is) it runs for every
> long time already, which in production application it will only get worse
> if sky adoption grows.
>
> this is now top priority as well -- but we cannot sacrifice type checks etc.
> all sky provides strong guarantee, so we need industrial deep dive +
> research + analysis and fix this issue once and for all

> Build speed (the private web:app): Spot VM, 8 CPUs, 32 GB: 29 minutes. My
> Mac: about 25 minutes. GitHub runner: 30 to 38 minutes.

> please, this is top priority right now, and you will manage e2e for speed up
> with no quality/checks sacrificed basically. leveraging multi-core, caching
> + split go modules strategies all you found/researched to improve sky's
> build time for various targets.
>
> i'm going to be away, so unless you have questions for me, otherwise please
> start in 100% unattended + autonomous + PIV mode
> the end goal is simple: speed up build time as much as possible within
> safety boundary.
>
> once improved, tested properly to ensure no regression on built apps
> essentially, then push/merge branch work into main + cicd green + tag
> released

## Done means (all of it, judged by an independent Judge)

1. Build time of Sky apps falls as much as possible, on every target (Sky.Live
   `web`, Sky.Spa `web:app`, native shells), using multi-core, caching and
   split Go packages.
2. No type check, guarantee or quality gate is weakened.
3. Tested properly: no regression on built apps.
4. Merged to `main`, CI green (the full release workflow), and a release tag
   cut.

## Shipping scope decision (user, 2026-10-15)

After the first Judge verdict, the user was asked whether v0.27.9 must also
carry the per-module Go package split, a parallel (multi-core) front end and a
linear unifier, or ship with a measured decision record that defers them. The
user chose: "Ship now, record". So v0.27.9 ships the build-speed work done,
plus the mobile-shell fix, a benchmark of the final commit and a committed
decision record (`docs/perf/build-speed-decisions.md`) that states, with its
numbers, why each deferred lever is deferred. This is a shipping-scope decision
by the user, not a change to the goal above; the Judge reports the literal and
the scoped verdicts separately.
