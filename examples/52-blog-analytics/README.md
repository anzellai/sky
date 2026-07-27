# 52 — Blog SPA with admin + product analytics

A small but **production-shaped** app: a server-driven blog SPA with a
bcrypt-gated admin dashboard and built-in product analytics. Every
capability comes from the Sky standard library — no user-written FFI, no
glue code, no external services to stand up.

It exists to answer one question: *what does a real Sky app look like when
the batteries are actually wired together?*

## What it shows

| Concern | Sky feature | Where |
|---|---|---|
| Server-driven SPA (one SSE, `sky-nav` links, URL routing) | **Sky.Live** | `main`, `view` |
| Typed layout, no CSS | **Std.Ui** | `view` and friends |
| Content in SQLite (typed queries, params) | **Std.Db** | `loadPosts` / `loadPost` / `seed` |
| Admin login (bcrypt, never plaintext) | **Std.Auth** | `adminHash` / `checkAdmin` |
| Auto page-views, consent-gated | **Std.Analytics** | `analytics = { pageViews = True }` |
| Admin analytics dashboard (totals / counts / recent) | **Std.Analytics** queries | `loadTotal` / `loadCounts` / `loadRecent` |

## The production wiring lives in `sky.toml`

Nothing about persistence or the analytics store is hard-coded in the app —
it is configured, exactly as you would for a real deployment:

```toml
port = 8052

[database]        # blog content
driver = "sqlite"
path = "blog.db"

[analytics]       # where captured events are stored
dbPath = "analytics.db"
```

Drop the `[analytics] dbPath` line and analytics **reuses the console DB**
(`SKY_CONSOLE_DB_PATH`) automatically — the main app is the sole writer,
the console reads. Point `[database]` at Postgres for a multi-instance
deploy and the app code does not change.

## Analytics is default-safe

- **Anonymous by default.** Page-views are captured with a random
  anonymous id and *no* identity until the visitor accepts the consent
  banner (`setConsent Granted`).
- **`Denied` drops all capture.** Consent is session-scoped, so one
  visitor's choice never leaks into another's.
- Identity only ever enters analytics through `identify` + the `Pii`
  type — never as a stray `String`.

## Run it

```bash
cd examples/52-blog-analytics
BLOG_ADMIN_PASSWORD=secret123 sky run src/Main.sky
# open http://localhost:8052
#   /               → home (two seeded posts)
#   /post/hello     → a post
#   /admin          → sign in (password from BLOG_ADMIN_PASSWORD; default admin123)
#                     then see the analytics dashboard
```

The admin dashboard shows total events, per-event counts, and the recent
event stream — read straight from the analytics store, the same data the
Sky Console renders.
