# 57 — Reactive todos on SQLite (write-layer change source)

Identical to `56-reactive-todos` but backed by **SQLite** (`connectRelational`)
instead of BlueDB. Proves reactive queries are **backend-agnostic**: SQL has no
engine change-feed, so the Persist write layer publishes each change to the same
broker topic — `watchCollection` works the same, browser-verified at ~70ms.

Single line changed vs the KV version: `connectKeyValue` → `connectRelational`
(plus a boot `Persist.create`). Run:

```bash
DATABASE_URL="sqlite:data/todos.db" sky run src/Main.sky
```

Two-session e2e: `scripts/verify-reactive-todos.sh` (point `REACTIVE_PORT` at
either backend).
