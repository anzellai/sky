# 53 — BlueDB persistence + migration

A minimal Sky.Live app whose **entire Model is persisted to BlueDB** — so the
counter survives a server restart — plus a worked **schema-migration** story for
when the Model shape changes between deploys.

```
Live.app (config { … } |> Live.autoBlueDB |> Live.withMigrate migrate)
```

- `Live.autoBlueDB` — the session store is BlueDB (embedded, group-committed,
  single-file, durable). No `[database]`, no external service. The `Model` *is*
  the database.
- `Live.withMigrate migrate` — a `Model -> Model` fixup run once when a session
  is **resumed** from a Model that an *older build* serialized.

## Run it

```bash
cd examples/53-bluedb-migration
sky run src/Main.sky              # → http://localhost:8000
```

Click **+** a few times. Then stop the server (`Ctrl-C`) and start it again:

```bash
sky run src/Main.sky
```

Reload the page — **the count is still there.** It was never in RAM only; every
update was group-committed to `data/app.blue`. Delete that file (or run
`sky db reset`-style: `rm -rf data/`) to start fresh.

> The store lives at `data/app.blue` (set by `storePath` in `sky.toml`). The
> runtime creates the `data/` directory for you on first run.

## The migration story

`Model` here is `{ count : Int, theme : String }`. Pretend `theme` was **added in
a later deploy** — call the builds *v1* (`{ count }`) and *v2* (`{ count, theme }`).

BlueDB serializes the Model with Go's `gob`, which matches fields **by name**:

| Change between deploys | What happens on resume |
|---|---|
| **Additive** — new field (`theme`) | Session **resumes**; `count` is preserved, the new `theme` comes back as its **zero value** (`""`) — *not* `init`'s default. |
| **Breaking** — a field's type changes, or a field is removed and re-added with a new type | `gob` decode fails → the session is **cleanly reset to `init`** (never a crash). |

So a v1 session that resumes under v2 arrives with `theme = ""`. That's where
`withMigrate` earns its keep:

```elm
migrate : Model -> Model
migrate model =
    if String.isEmpty model.theme then
        { model | theme = "light" }   -- v1 had no theme → default it

    else
        model
```

`migrate` runs on **every** resume, so keep it **idempotent** — guard each fix on
a sentinel (here: "is `theme` empty?"), never do an unconditional bump.

### Verified end-to-end

This exact flow is tested: build v1 (`{ count }`), increment to 5, then deploy v2
(adds `theme` + `withMigrate`) against the **same** store and session cookie →

```
v2 resumed same session → count=5  theme=light
```

`count=5` survived the deploy (durable), and the brand-new `theme` field resumed
as `""` and was defaulted to `"light"` by `withMigrate`.

## When to reach for what

- **Session Model** (this example) — ephemeral-but-durable UI state per user.
  `autoBlueDB` + optional `withMigrate`. A breaking change safely resets a
  session; that's usually fine for view state.
- **Your app's own records** (users, orders, …) — use **`Std.BlueDB`** (a typed
  KV keyed by your Codec) or `Std.Db.Store`. There, treat the **DB as the source
  of truth** and the Model as a cache: migrate the *stored records* with a
  versioned Codec, not the Model. See `docs/bluedb/migration.md`.

The rule of thumb: **Model is ephemeral, the store is the source of truth.** Keep
durable business data in `Std.BlueDB`/`Std.Db.Store` records with explicit
versioning; let the session Model be a convenience that can always be rebuilt.
