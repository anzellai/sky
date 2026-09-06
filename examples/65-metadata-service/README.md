# 65 — metadata-service

A headless, **stateless** JSON API backed by `Std.Db` reads — the shape of an
internal "core-metadata" service: `Sky.Http.Server`, no Sky.Live, no SSE, no
sessions. Every request is HTTP → one SQL read → JSON.

This example is also the **capacity baseline** for the headless-API shape; the
measurement lives in [`docs/perf/http-metadata-service-capacity.md`](../../docs/perf/http-metadata-service-capacity.md).

## Endpoints

| Method + path | Behaviour |
|---|---|
| `GET /healthz` | `{"status":"ok"}` — no DB touch |
| `GET /metadata/:key` | one row by primary key, or `404` |
| `GET /metadata?limit=N` | first N rows, ordered by key (default 50) |

## Run

```bash
# embedded PostgreSQL 18.6 (sky run supervises a per-project cluster), binds :8137
sky run src/Main.sky

# in another shell:
curl -s http://127.0.0.1:8137/healthz
curl -s http://127.0.0.1:8137/metadata/svc-0042
curl -s 'http://127.0.0.1:8137/metadata?limit=3'
```

The table (`metadata`, PK on `key`) is created and seeded with 500 rows at
startup — both steps are idempotent (`CREATE TABLE IF NOT EXISTS` +
`INSERT … ON CONFLICT DO NOTHING`), so restarts are safe.

Port 8137 (not the stdlib-default 8000) is a literal in `src/Main.sky`
(`servicePort`).

## Load harness

`load/loadgen.go` is a stdlib-only closed-loop HTTP load generator; it reports
req/s, p50/p90/p99 latency and error rate per concurrency level.

```bash
./load/run-load.sh                          # sweep :key, ?limit=50, /healthz
URL=http://<host>:8137 ./load/run-load.sh   # a remote target (unchanged app)
LEVELS="512,1024,2048,4096" DUR=8s ./load/run-load.sh
```
