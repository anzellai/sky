# Sky Console v0.16.x — Migration guide

> How sky-lang.org, skydeploy, and ringfence (the three real test apps)
> adopt v0.16.x. Plus general guidance for any Sky app upgrading from
> v0.15.x.

## Compatibility commitment

v0.16.x is backwards-compatible with v0.15.x at the cfg + env-var surface:

- Existing `Live.app` cfg keeps building unchanged.
- Existing env vars (`SKY_ADMIN_TOKEN`, `SKY_CONSOLE_TOKEN_SECRET`, `SKY_CONSOLE_DB_PATH`, `SKY_CONSOLE_EMBED`) still honoured as back-compat aliases.
- New env vars are additive; defaults preserve v0.15.x behaviour.

An app that does NOTHING on upgrade gets the same behaviour as v0.15.x (embedded console in dev, off in production unless `SKY_CONSOLE_EMBED=on`).

## Upgrade path for any Sky app

**Step 1: bump compiler.**
```bash
sky upgrade   # picks up v0.16.x release
sky build src/Main.sky
```

That's the whole upgrade. App behaviour identical to v0.15.x unless you opt into new features.

**Step 2 (optional): adopt new auth gate.**

Production apps gain explicit auth choice:
```
SKY_CONSOLE_AUTH=token    # default — single env-var token
SKY_CONSOLE_AUTH=app      # use Live.app's consoleAuth callback
SKY_CONSOLE_AUTH=off      # explicit opt-out (was implicit before)
```

Pick whichever fits. For SSO-gated dashboards, use `app` mode and add the `consoleAuth` field on `Live.app` cfg.

**Step 3 (optional): push to a hub.**

Set `SKY_CONSOLE_HUB` + `SKY_CONSOLE_HUB_TOKEN`. Telemetry now flows to the hub in addition to the local embedded console.

**Step 4 (optional): disable embedded.**

If the hub is the only thing you want to look at: `SKY_CONSOLE_EMBED=off`. Telemetry flows only to the hub.

## sky-lang.org migration

Currently (post-2026-06-02): runs Mode B (exporter to GCP Cloud Logging + Cloud Monitoring via Ops Agent). Embedded console disabled (`SKY_CONSOLE_EMBED=off`) due to v0.15.x's OOM problem.

**Post-v0.16.0 (embedded hardening):**
```dotenv
# Re-enable embedded console — now works on e2-micro
SKY_CONSOLE_EMBED=on
SKY_CONSOLE_AUTH=app                          # use GitHub session
# Drop SKY_CONSOLE_TOKEN_SECRET — no longer needed for JWT-mint
```

In `src/Main.sky`:
```elm
main =
    Live.app
        { ... existing fields ...
        , consoleAuth = AuthConsole.handler        -- existing helper, simplified
        }
```

Delete `src/Auth/Console.sky`'s 100-line JWT-mint code. Replace with a simple identity-lookup that returns the validated session as `Identity`. The framework does the rest.

**Post-v0.16.2 (hub available):**

Option A: keep GCP-native (Cloud Logging / Cloud Monitoring) for production observability. Embedded console for local debug.

Option B: stand up a Sky Console hub on a separate e2-small VM (`obs.sky-lang.org` perhaps). Migrate from GCP-native to hub-based.

Option C: do both. Push to hub for unified pane across all of operator's apps; keep GCP-native as a redundant backup.

Recommend Option C for sky-lang.org (the operator runs multiple apps; the hub pays off).

**Cleanup post-migration:**
- Remove Ops Agent install from `deploy/setup-remote.sh` if Option B (saves ~80 MB RAM on the VM)
- Or keep it if Option C
- IAM roles `roles/logging.logWriter` + `roles/monitoring.metricWriter` + `roles/cloudtrace.agent` can be revoked if pure Option B

## skydeploy migration

Currently: each per-tenant Cloud Run service has its own `/data/console.db` with Litestream replication to GCS. Console served per-tenant via JWT-in-URL → cookie pattern from the SkyDeploy control plane.

**v0.16.0 deliverable**: per-tenant Cloud Run services continue to work as today. JWT-in-URL gets hardened (one-shot, `aud` claim check) but the contract is preserved.

**v0.16.1 (exporter)**: per-tenant services start pushing to a SkyDeploy-hosted hub. Each tenant gets a unique `SKY_CONSOLE_HUB_TOKEN`. The Litestream pattern can be deprecated:

Old:
```
Tenant Cloud Run → /data/console.db → Litestream → GCS (per-tenant bucket)
                ↓
                JWT-in-URL → SkyDeploy control plane renders iframe
```

New:
```
Tenant Cloud Run → in-process exporter → hub.skydeploy.app:4317
                                              ↓
                                          DuckDB store (multi-tenant, ACL by service.name)
                                              ↓
                                          SkyDeploy control plane renders dashboard for tenant
```

Migration timeline:
- v0.16.1 lands → tenants opt into hub push (add env vars), Litestream stays as fallback
- v0.16.5 lands → SkyDeploy control plane uses hub-backed dashboard by default
- v0.17 → deprecate per-tenant Litestream entirely, tenants get a fresh per-tenant view from the hub

**Wins for SkyDeploy operator:**
- No per-tenant GCS bucket
- No per-tenant Litestream config
- One unified hub to operate, easier to scale
- Tenants get cross-deployment view (compare versions, see history of all deploys) — was hard with per-tenant DB

**Wins for tenants:**
- Faster console (hub queries DuckDB columnar store, vs SQLite query on cold-cache restore)
- Multi-app view if a tenant operates multiple SkyDeploy apps
- Lower per-instance RAM (no in-process SQLite write-through)

## ringfence migration

Currently: separate productionised app on settleby GCP (saw `ringfence-cloud` firewall tag during sky-lang.org provisioning).

ringfence joining the unified hub is straightforward:
1. Set `SKY_CONSOLE_HUB` + `SKY_CONSOLE_HUB_TOKEN` in ringfence's env
2. Rebuild + redeploy
3. ringfence telemetry appears in the same hub UI alongside sky-lang.org + skydeploy

No code changes in ringfence.

## When NOT to migrate

Reasons to stay on v0.15.x or Mode B (external observability) for a given app:
- App is in a regulated environment that requires Datadog/CloudWatch/Splunk
- Team is already invested in Grafana + Loki + Tempo
- App's host has no spare RAM (sub-512 MB total) — embedded console even at v0.16's reduced footprint may not fit
- App doesn't generate enough telemetry to justify the operational surface (e.g. weekly batch job)

v0.16.x doesn't force adoption. The defaults preserve v0.15.x behaviour; opt-in to each new mode.

## Migration validation checklist

Per-app:
- [ ] `sky build` succeeds with v0.16.x compiler
- [ ] App starts on production VM/container without OOM
- [ ] `/_sky/console` accessible (if embedded enabled) and gated correctly
- [ ] `sky_telemetry_dropped_total` stays at 0 under steady load
- [ ] Hub receives data (if hub configured) — verify in hub UI
- [ ] Existing alerting / monitoring continues to work
- [ ] Logs still flow to legacy destination (if external observability also configured)

Hub-side:
- [ ] `sky console serve` running on dedicated VM
- [ ] All expected services visible in dashboard
- [ ] DuckDB warm store growing as expected (~10-50 MB/day per active app)
- [ ] Litestream replication healthy (if configured)
- [ ] Per-tenant ACL working (if multi-tenant)

## Rollback plan

If v0.16.x causes issues:
1. `sky upgrade` to the previous v0.15.x release
2. Restart apps — they read the old binary's behaviour
3. v0.15.x can't read v0.16.x's `<projectName>.console.db` schema if v0.16.5 hot/warm split landed — but v0.16.0's schema is identical to v0.15.x, so embedded console state is preserved across rollback
4. Hub data: if hub-mode was enabled, telemetry pushed to the hub stays in the hub regardless of app rollback. No data loss from rolling app back; just stop pushing temporarily.

## FAQ

**Q: Does v0.16.x break my existing CI / deploy scripts?**
A: No. Env vars are additive. CI that does `sky build && go test` continues to work.

**Q: Do I need to migrate to a hub to benefit from v0.16.x?**
A: No. v0.16.0 fixes the embedded console for single-VM deploys. Hub is opt-in for multi-app scenarios.

**Q: What's the minimum sky version for a hub?**
A: v0.16.2+ for `sky console serve`. Apps pushing to it can be v0.16.1+. Cross-version compatibility within v0.16.x is guaranteed.

**Q: How long should I leave Mode B (external observability) on alongside hub?**
A: Recommend ≥ 2 weeks of overlap. Validate the hub captures everything before deprecating the external path.

**Q: Will my Cloud Logging / CloudWatch costs drop?**
A: If you stop pushing telemetry to them — yes, significantly. Cloud Logging at 50 GB/month free is plenty for most teams once they move to the hub. CloudWatch / Datadog: linear $$$ savings.
