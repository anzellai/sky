# Sky Console v0.16.x — Production operations

> Alerts, RBAC, retention, replication, query keyword filters.
> Lands in v0.16.5 — the production-polish patch.

## Alerts

Goal: operators get notified when something's wrong without watching the dashboard.

### Rule shape — YAML, not in the UI (v0.16.5)

Alert rules are declared in a YAML file the hub reads at boot:

```yaml
# /etc/sky-console/alerts.yaml
rules:
  - name: high-error-rate
    service: "*"                              # applies to all services
    expression: "rate(errors, 5m) > 0.05"     # >5% error rate over 5 min window
    for: 2m                                    # must hold for 2 min before firing
    severity: warning
    channels: [slack-ops]

  - name: hub-itself-degraded
    service: "_hub"                            # the hub observes itself
    expression: "sky_telemetry_dropped_total[5m] > 1000"
    severity: critical
    channels: [pagerduty]

  - name: ringfence-billing-stale
    service: "ringfence"
    expression: "absent(billing_sync_success, 1h)"   # no success in 1 hour
    severity: critical
    channels: [slack-ops, pagerduty]
```

Expression syntax: a small DSL — `rate(name, window)`, `quantile(name, p, window)`, `absent(name, window)`, basic arithmetic. NOT PromQL (deferred). The DSL covers ~90% of real alert use cases; the remaining 10% can write a custom Sky function that's invoked by the hub (escape hatch, v0.16.5+).

### Notification channels

```yaml
# /etc/sky-console/channels.yaml
channels:
  - name: slack-ops
    type: slack
    webhook_url: ${SLACK_WEBHOOK_URL}
    rate_limit: 1/min                     # dedupe burst alerts

  - name: pagerduty
    type: pagerduty
    routing_key: ${PAGERDUTY_KEY}

  - name: email-anzel
    type: email
    to: anzel.lai@gmail.com
    from: alerts@obs.sky-lang.org

  - name: webhook-generic
    type: webhook
    url: https://your-incident-tool.com/api/v1/incidents
    auth_header: "Bearer ${TOKEN}"
```

Built-in: Slack, PagerDuty, email, generic webhook. Custom: webhook is the escape hatch — point at your own incident-management tool.

### Alert state + UI

Hub UI gains an "Alerts" section in the header (badge with active count). Click → list of currently-firing rules with: rule name, service, value, duration, link to context (requests/logs/traces for that window).

History: last 30 days of alert state changes stored in the hub DB, queryable.

Future (v0.17+): UI for editing rules; for v0.16.5, edit YAML + SIGHUP the hub.

## RBAC

Goal: in multi-tenant deploys (SkyDeploy hosting customer hubs), each user sees only their data.

### Identity → Role → Permission model

The `consoleAuth` callback returns an `Identity`:

```elm
type alias Identity =
    { subject : String                                -- user ID
    , email : String
    , claims : Dict String String                     -- extra attrs
    }
```

The hub's RBAC layer maps claims to roles:

```yaml
# /etc/sky-console/rbac.yaml
roles:
  - name: admin
    services: ["*"]                            # all services
    actions: ["read", "write", "manage"]       # all actions

  - name: tenant-read
    services:                                  # services matching template
      - "${claims.tenant}-*"                    # interpolated from identity claims
    actions: ["read"]                          # read only

  - name: ops-on-call
    services: ["*"]
    actions: ["read", "ack-alerts"]            # can ack alerts, can't reconfigure

mappings:
  - if: { claim: "tenant", set: true }         # any user with a tenant claim
    role: tenant-read

  - if: { claim: "role", value: "ops" }
    role: ops-on-call

  - if: { email_domain: "settleby.com" }       # internal users
    role: admin
```

The framework enforces: every query passes through `enforceACL(identity, roles, query)` which prunes the query's service list to the identity's allowed services.

### Audit log

Every dashboard request logged with `(user_id, tenant, query, response_count, duration)`. Stored in `console-audit.db` (separate from the data store; retained 90 days).

Use cases: "who accessed customer-42's data last week?", incident response, compliance.

## Retention + replication

### Retention defaults (configurable per signal)

```yaml
# /etc/sky-console/retention.yaml
hot:
  duration: 24h                       # SQLite, all signals
warm:
  duration: 30d                       # DuckDB, rolled up + sampled

sampling:
  spans:
    error: 100%                       # never drop error spans
    slow_p95: 10%                     # keep 10% of slow spans
    fast: 1%                          # keep 1% of fast spans
  logs:
    error: 100%
    warn: 100%
    info_in_warm: false               # don't roll INFO logs to warm
    debug_in_warm: false              # don't roll DEBUG to warm at all
  metrics:
    raw_in_warm: false                # only 1m rollups in warm
    rollup_window: 1m
```

Defaults are opinionated (per `EXPORTER.md`'s sampling table). Sites can override per-service for high-value services that need full fidelity.

### Litestream replication

The hub's SQLite hot store is continuously streamed to S3/GCS via Litestream:

```yaml
# /etc/sky-console/replication.yaml
hot_store:
  bucket: gs://your-sky-console-backup
  region: us-central1
  retention: 7d              # keep 7 days of replicated checkpoints
warm_store:
  # DuckDB doesn't benefit from streaming WAL; use scheduled snapshots
  bucket: gs://your-sky-console-backup
  schedule: "0 2 * * *"      # nightly 2 AM
  retention: 30d
```

Recovery from disaster:
1. Hub VM dies entirely
2. Provision new VM, install Sky
3. `sky console restore --from gs://your-sky-console-backup`
4. Latest checkpoint of hot store + most recent warm snapshot restored
5. Apps reconnect to the new hub — telemetry flow resumes
6. Recovery RPO: ~1 minute hot, ~24 hours warm (typically)

Existing pattern in SkyDeploy (Phase 3d in CLAUDE.md); v0.16.5 generalises to the hub.

## Query keyword filters (v0.16.5)

Hub UI's existing keyword + time + level filters get an expression layer for power users:

```
service:sky-lang.org AND level:error
duration:>500ms route:/api/*
trace:abc123 OR trace:def456
"specific error message"
errId:ab12cd34
```

Not PromQL/LogQL — closer to GitHub's issue search DSL. Implemented as a simple tokenizer + SQL builder. The DSL parses into a SQL `WHERE` clause against the hot/warm stores.

This is the keyword-query layer that 95% of real ops queries can express. The other 5% can drop to raw SQL via the hub's admin API (v0.17+).

## Hub-on-hub: observing the hub itself

The hub IS a Sky app. Its OWN embedded console is mounted at `/_hub/console` (different path to avoid collision with user services).

The hub also pushes its own telemetry to itself (loopback OTLP) and tags with `service.name="_hub"`. So the hub UI shows the hub's own health alongside everything else.

Self-observability matters: if the hub starts dropping telemetry due to ingest backpressure, operators see it in the same dashboard they're already using.

## Litestream alternative: just take backups

For teams that don't want continuous replication:
- Daily `cp /var/lib/sky-console/*.db /backup/` via cron
- Acceptable RPO: 24 hours
- Cheaper than Litestream (no continuous transfer), simpler ops
- Worse for incident response (lose the last 24 h of context)

Litestream is the recommended default; manual backups are a documented fallback.

## Resource scaling on the hub

When the hub starts to feel pressure:

| Symptom | Likely cause | Fix |
|---|---|---|
| Ingest latency > 100ms | Receiver CPU saturation | Beefier VM (e2-small → e2-medium) |
| Query latency > 2s on warm store | DuckDB scan size | Tighter retention, more aggressive sampling |
| Disk > 80% full | Retention too long for storage size | Reduce warm retention OR scale up disk |
| `sky_telemetry_hub_dropped_total` rising | Backpressure on hot store writes | Move to PostgreSQL/ClickHouse backend (v0.17+) |
| Hub OOM | DuckDB query cache too large | Set `SKY_CONSOLE_HUB_DUCKDB_MAX_MEM` |

v0.16.x stays single-VM. Sharding the hub across multiple instances (by `service.name` hash, say) is v0.17+ work.

## Implementation milestones (v0.16.5)

| Day | Work |
|---|---|
| 1 | Alert rule parser + evaluator. YAML loading, SIGHUP reload. Built-in expression DSL. |
| 1 | Notification channels: slack + pagerduty + email + webhook. Rate-limiting + deduplication. |
| 2 | RBAC enforcer + audit log. Wire into all hub UI queries. |
| 2 | Retention config — replace hardcoded defaults with YAML-driven. Per-service overrides. |
| 3 | Litestream integration for hot store. DuckDB scheduled snapshot for warm. Restore CLI command (`sky console restore`). |
| 4 | Keyword query DSL — tokenizer + SQL builder. Hook into hub UI's filter inputs. |
| 5 | Self-observability — hub pushes own metrics to itself. Polish. End-to-end validation on sky-lang.org + skydeploy + ringfence. |

## Operational checklist for v0.16.5 hub

Before production:
- [ ] `alerts.yaml` reviewed by team; rate limits set
- [ ] `channels.yaml` tokens registered with destination services
- [ ] `rbac.yaml` reviewed; default `mappings:` order checked (first match wins)
- [ ] Litestream bucket provisioned with lifecycle rules
- [ ] Restore drill performed: take down a staging hub, restore from backup, verify recovery RPO
- [ ] Audit log retention configured per compliance requirements
- [ ] Self-observability dashboard pinned to hub UI homepage
