# 63 · Sky.Spa Chat — real-time group chat (server→client push, one source)

A real-world **group chat**: a message sent by ANY client appears **live on every
connected client**, with no reload. A dark header, a scrollable message panel of
own-vs-other bubbles with timestamps, and a pinned composer — rich `Std.Ui`
throughout (`vh`/`fill`/`maximum` sizing, a `scrollbarY` history, alignment,
colours/borders/rounding/shadow) — backed by **SQLite persistence**.

This is the **auto-split tier**. You write **ONE** Sky.Spa project with effects
inline; `sky spa-split` derives the wasm frontend, the native SQLite backend, and
the shared wire contract. **Effectful branches become RPCs; pure UI branches stay
client-local; `Cmd.publish`/`Sub.subscribeTopic` become an SSE broadcast bus.**
You never hand-write a client/server boundary — or the real-time plumbing.

## The one project

```
src/
  Main.sky      -- Model / Msg / init / update / view / subscriptions / main
  Domain.sky    -- the pure Message type + its Codec  (PURE → copied into BOTH trees)
  Persist.sky   -- dbConn + messagesStore + loadHistory/appendMessage
                --   (EFFECTFUL → the whole module routes BACKEND-ONLY)
```

The split is **inferred** — `pure → client, ANY effect → server`:

| `update` branch | verdict | why |
|---|---|---|
| `Compose` `SetName` `Received` `Ignore` | **CLIENT** | pure — edit the draft/name, fold a pushed message, `Cmd.none`, zero network |
| `Load` `Send` | **SERVER** | reach `Persist.*` → `Db_*` / `Time.now` kernels → become `POST /_rpc/<Msg>` |

Inspect it yourself:

```bash
sky spa-partition src/Main.sky        # prints each branch CLIENT/SERVER + read/write sets
```

```
SERVER  Load        in: {}                        out: {messages}
                    references Persist.loadHistory (reaches server kernel Db.queryObjects)
SERVER  Send        in: {draft, name}             out: {draft}
                    references Persist.appendMessage (reaches server kernel Time.now)
Server-tainted top-level bindings: appendMessage, dbConn, loadHistory, messagesStore, nowMillis
```

Because `Persist` holds every effect, the **whole module is routed backend-only**
and is never emitted into — or imported by — the wasm frontend. The security spine
of the split: no store handle, connection, or secret can reach the browser.

## The real-time push — the whole point

A group chat is only a chat if a message from one client reaches the others *now*.
The auto-split wires that from two ordinary stdlib calls — **you write no SSE, no
broker, no EventSource**:

- `update`'s `Send` branch persists the line, then returns
  `Cmd.publish "room:main" (Codec.toJson messageCodec saved)`.
- `subscriptions` returns `Sub.subscribeTopic "room:main" onPush`.

Seeing `publish` + `subscribeTopic`, `sky spa-split` prints

```
server->client PUSH enabled: mounted `GET /_sky/sub` (SSE) + a shared broker;
RPC handlers fan their returned Cmd.publish through it.
```

and generates, on the **backend**: a shared in-process broker, an SSE endpoint
`GET /_sky/sub?topic=…`, and RPC handlers that feed each returned `Cmd.publish`
through the broker. On the **frontend**: the `subscribeTopic` leaf becomes an
`EventSource("/_sky/sub?topic=room:main")` whose every frame is decoded and
dispatched as `onPush → Received`.

The wire path, end to end:

```
client A: Send ─▶ POST /_rpc/Send ─▶ backend persists (SQLite) + returns
                                     (m, Cmd.publish "room:main" json) ─▶ broker
broker.Publish("room:main", json) ───────────────────────────────────────┐
                                                                          ▼
every client on GET /_sky/sub?topic=room:main gets `data: <json>\n\n` ─▶
EventSource.onmessage ─▶ JSON→Sky ─▶ onPush ─▶ Received msg ─▶ append (dedup on id)
```

**No double-append, one code path.** `Send` deliberately does **not** append
locally. `Cmd.publish` echoes to the publisher too, so the sender receives its own
message back over SSE and appends it through the exact same `Received` path as
everyone else — deduped on the DB-assigned `id`, so it lands **exactly once**.

**The payload is a JSON string.** `publish`/`subscribeTopic` payloads are `any`;
we encode a `Message` with `Codec.toJson messageCodec` on publish and decode it
with `Codec.fromJson messageCodec` on receive, so the value round-trips through one
codec with no ad-hoc `any` handling.

**Single replica vs cross-replica.** The default broker is **in-process** — a
publish on this instance reaches only the SSE connections on this instance, which
is exactly right for a single-instance deploy. For **multiple replicas**, pass a
Redis URL so a publish on replica A reaches an SSE subscriber on replica B:

```bash
sky spa-split src/Main.sky --out .split --build --broker redis://localhost:6379
# or, at runtime:  SKY_LIVE_BROKER_URL=redis://…   (env overrides the baked URL)
```

This is the same cross-instance broker Sky.Live uses; no shared session store is
required (the broker is app-scoped). A multi-replica deploy also needs sticky
routing so a client's `/_sky/sub` and `/_rpc/*` hit a coherent instance.

## Build + run

```bash
sky spa-split src/Main.sky --out .split --build      # derive + build both projects
cd .split/backend && SKY_DB_PATH="$(pwd)/chat.db" ./sky-out/app
# open http://localhost:8972/ in TWO tabs — send in one, watch it appear live in the other
```

…or just `./run.sh` (it runs exactly that, guarding compiler freshness first, and
documents the `--broker` flag for cross-replica fan-out). The backend serves the
frontend **and** `/_rpc/*` **and** `/_sky/sub` **same-origin** from one binary, so
there is no CORS and a trivial CSP. The **SQLite history persists** across server
restarts and page reloads — a boot `Load` RPC re-hydrates the panel on load.

What each tree gets:

- **`.split/frontend/`** — `Main` (view + pure branches; server branches rewritten
  to `Spa.postJson … "/_rpc/<Msg>" … Applied<Msg>`, `subscribeTopic` kept verbatim)
  + `Domain` + generated `Shared`. Built `--target web` →
  `dist/{index.html, main.wasm, wasm_exec.js}`. Leak-check is clean:
  `grep -rnE 'Db\.|Store|Task\.run|System\.' .split/frontend/src/` → nothing.
- **`.split/backend/`** — `Main` (a `Server.listen` with one `POST /_rpc/<Msg>` per
  server branch + `GET /_sky/sub` + the broker) + `Domain` + `Persist` + `Shared` +
  `Server.static "/" "../frontend/dist"`.

## Tier

This is a **client + server, real-time** example. As shipped it is a tier-1 setup
— SQLite single-file store, in-process broker, `memory` Sky.Live session store, no
auth — perfect for a single-instance deployment. For production: switch the store
to PostgreSQL (`sky db provision --embed`, or a `DATABASE_URL`), pass a Redis
`--broker` for cross-replica push, and add `Std.Auth`; the app code is unchanged
because `Std.Db` is dialect-safe and the broker is chosen at split/deploy time. The
client stays 100% pure UI either way — every effect already lives on the server
side of the split.
