# Autonomous goal: Sky v0.29.1

Branch `fix/v0.29.1`, from the v0.29.0 merge on `main` (2b89d90da).
Private context: `.claude/AUTONOMOUS_GOAL.private.md` (gitignored).
The previous mandate (v0.29.0 real-world capabilities) is complete: v0.29.0
was tagged on 2b89d90da and published on 2026-10-31.

## The user's words (2026-10-31)

- "For 0.29.1 there's a possible critical bug we found from my private ...
  app" (a Sky.Spa form submitted before the client hydrates ends on a 403
  JSON page). "Ok 0.29.1 that is."
- "after v0.29.0 is released, please refer to ... a otel metrics/tracing etc.
  for request path or querystring with sensitive info like token, secret path
  link, or secrets/PII on querystring. that will be for 0.29.1 (together with
  anything we found issues against 0.29.0)"
- On a database-versus-code drift tool: "Yes 0.29.1, and should use --sql and
  --code flag? Much more intuitive"
- "If this round of checks still found issue unrelated to this branch, I
  reckon we should do a release. And patch all findings in 0.29.1"
- "You have my permissions to run rollback + start all the fixes"

## Scope

1. A regression found in production on v0.29.0: `Store.defaultWith` (and the
   other default builders) on a column that is in the table but not in the
   record codec is refused, so every Store call on that table fails and the
   app shows empty lists. That use was valid in v0.28. Restore it, with a
   test. A Store builder problem must never make a page empty without a loud
   error the operator can see.
2. Sky.Spa: a form submitted before the client hydrates must not end on a
   403 JSON page. Hold and replay the submit after hydration, inject the CSRF
   field on the Spa SSR page (first visit included), and answer a native POST
   to a Spa page route with 303 to the same URL.
3. OTel / metrics / traces / logs: never record secrets or personal data
   from a request path or query string (tokens, secret path links, PII).
4. Moved to v0.29.2 by the user ("If sky dB check takes considerable amount
   of time, leave that for 0.29.2 the others are more priority to rollout +
   migrate all apps"): `sky db check`, `--sql`, `--code`.
5. Every finding deferred from v0.29.0 (listed in the private companion).
6. Added by the user (2026-10-31): "the header case sensitive get/check isn't
   consistent. so checking/getting like X-Sky-Api-Key very often incorrectly if
   with x-sky-api-key or X-SKY-API-KEY etc. could you check and fix if possible
   too?" Every place Sky reads or checks an HTTP header name must match it
   without regard to case.

## Done means

All of the above built, tested (a failing test first for each bug), documented
(CHANGELOG, migration guide where behaviour changes, docs, templates), both
`rust-ci.yml` and `release.yml` green on one SHA, and a fresh-context Judge
passing. Then tag v0.29.1 and redeploy the apps.
