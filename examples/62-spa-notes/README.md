# 62 · Sky.Spa Notes — the auto-split (client + server, one source)

A real-world **Notes / knowledge-base** app: a dark header bar, a scrollable
sidebar of note cards with live search, and a full editor (title + growing
textarea + Save/Delete). Rich `Std.Ui` throughout — `vh`/`fill`/`px`/`minimum`
sizing, `scrollbarY` panels, a real `Ui.breakpoint Ui.mobile` responsive layout,
plus colours/borders/rounding/shadow — with **SQLite persistence**.

This is the **auto-split tier**. You write **ONE** Sky.Spa project with effects
inline; `sky spa-split` derives the wasm frontend, the native SQLite backend, and
the shared wire contract. **Effectful branches become RPCs; pure UI branches stay
client-local.** You never hand-write a client/server boundary.

## The one project

```
src/
  Main.sky      -- Model / Msg / init / update / view / main (the TEA loop + view)
  Domain.sky    -- the pure Note type + its Codec  (PURE → copied into BOTH trees)
  Persist.sky   -- dbConn + notesStore + load/create/update/deleteNote
                --   (EFFECTFUL → the whole module routes BACKEND-ONLY)
```

The split is **inferred** — `pure → client, ANY effect → server`:

| `update` branch | verdict | why |
|---|---|---|
| `Select` `DraftTitle` `DraftBody` `Search` | **CLIENT** | pure — pick/edit/filter in memory, `Cmd.none`, zero network |
| `Load` `Create` `Save` `Delete` | **SERVER** | reach `Persist.*` → `Db_*` kernels → become `POST /_rpc/<Msg>` |

Inspect it yourself:

```bash
sky spa-partition src/Main.sky        # prints each branch CLIENT/SERVER + read/write sets
```

```
SERVER  Create   in: {draftBody, draftTitle}   out: {draftBody, draftTitle, notes, selected}
                 references Persist.createNote (reaches server kernel Db.execObjectWith)
CLIENT  Search _  pure — no server effect or tainted value
Server-tainted top-level bindings: createNote, dbConn, deleteNote, loadNotes, notesStore, updateNote
```

Because `Persist` holds every effect, the **whole module is routed backend-only**
and is never emitted into — or imported by — the wasm frontend. The security
spine of the split: no store handle, connection, or secret can reach the browser.

## Build + run

```bash
sky spa-split src/Main.sky --out .split --build      # derive + build both projects
cd .split/backend && SKY_DB_PATH="$(pwd)/notes.db" ./sky-out/app
# open http://localhost:8971/
```

…or just `./run.sh` (it runs exactly that, guarding the compiler freshness first).
The backend serves the frontend **and** `/_rpc/*` **same-origin** from one binary,
so there is no CORS and a trivial CSP. The **SQLite DB persists** across server
restarts and page reloads (a boot `Load` RPC re-hydrates the list on load).

What each tree gets:

- **`.split/frontend/`** — `Main` (view + pure branches; server branches rewritten
  to `Spa.postJson … "/_rpc/<Msg>" … Applied<Msg>`) + `Domain` + generated `Shared`.
  Built `--target web` → `dist/{index.html, main.wasm, wasm_exec.js}`. Leak-check is
  clean: `grep -rnE 'Db\.|Task\.run|createNote|loadNotes' .split/frontend/src/` → nothing.
- **`.split/backend/`** — `Main` (a `Server.listen` with one `Server.api "POST
  /_rpc/<Msg>"` per server branch, reusing the app's own `init` + `update` to run
  the real effect) + `Domain` + `Persist` + `Shared` + `Server.static "/"
  "../frontend/dist"`.

## Tier

This is a **client + server** example. As shipped it is a tier-1 setup — SQLite
single-file store, `memory` Sky.Live session store, no auth — perfect for a
single-instance deployment. For production: switch the store to PostgreSQL
(`sky db provision --embed`, or a `DATABASE_URL`) and add `Std.Auth`; the app code
is unchanged because `Std.Db` is dialect-safe. The client stays 100% pure UI
either way — every effect already lives on the server side of the split.
