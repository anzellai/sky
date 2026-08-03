# BlueDB / Sky.Live — Model & data migration across a deploy

What happens to persisted state when you ship a new build whose **Model** (or a
stored **record**) changed shape — and how to control it. All of the behaviour
below is verified against the running compiler, not aspirational.

## The short version

| Change (across a deploy) | Persisted **session Model** (Sky.Live) | What to do |
|---|---|---|
| **Add a field** | Session **resumes**; the new field is its **zero value** (`0`/`""`/`False`/empty), *not* `init`'s default | `Live.withMigrate` to set the real default |
| **Remove a field** | Resumes; the old field is dropped | nothing |
| **Rename a field** | Resumes as add+remove: old value lost, new field zero | `withMigrate` if you need to carry the value |
| **Change a field's type** / restructure | gob decode fails → session **resets to `init`** (no crash) | expected; init re-runs (reload durable data from your DB) |
| Add / remove an ADT (union) variant | new variants fine; a *removed* variant present in an old blob → decode fails → reset | expected |

The Model is `gob`-encoded per session in the store (memory / sqlite / postgres /
redis / **bluedb**). gob matches by **field name**: it tolerates added/removed
fields and errors on a type mismatch. On a decode error the runtime treats the
session as absent and **creates a fresh one (`init`)** — graceful, never a crash.

## `Live.withMigrate` — fix up a resumed Model

The one sharp edge is *"additive change resumes with the new field at ZERO, not
`init`'s default."* `Live.withMigrate` runs your `model -> model` function on a
Model **resumed from the store**, once, before it's used — so you set the right
default (or coerce a renamed/restructured value):

```elm
main =
    Live.app
        (Live.config { init = init, update = update, view = view
                     , subscriptions = subscriptions, routes = routes
                     , notFound = Home }
            |> Live.autoBlueDB
            |> Live.withMigrate
                (\m ->
                    -- a v2 build added `theme`; resumed v1 sessions have "" → set it
                    if String.isEmpty m.theme then { m | theme = "light" } else m
                )
        )
```

Make it **idempotent** — it runs on *every* resume (there's no version counter),
so guard each fix on a sentinel (`if field == zero then set default`). It only
runs on resumes; a fresh session goes through `init`. (Verified e2e: a v1 session
with `count = 7` deployed against a v2 build that added `note` resumes with
`count = 7` and `withMigrate` sets `note`.)

## The discipline that makes migration a non-event

For a plain Sky.Live app, treat the **Model as ephemeral view state** and keep
the **source of truth in a DB** (`Std.Db` or `Std.BlueDB`). Then a Model reset on
a breaking deploy just re-derives from the DB on the next `init` — no data lost.
This is the Elm/Phoenix-LiveView model, and it's why "the Model reset" is usually
fine. Migrate your **DB schema** (versioned records / `Std.Db` file migrations),
not the Model.

## `Live.autoBlueDB` — when the Model IS the data

`autoBlueDB` persists the whole Model as the durable source of truth, so a Model
change *is* a data change:

- **Additive** changes are safe — resume + `withMigrate` to default the new field.
- **Breaking** changes reset the session to `init` (data for that Model lost). So
  during a breaking migration, either stage it additively (add new field, migrate
  data, remove old field in a later deploy), or accept the reset for transient
  state. First-class Model-schema versioning + transform hooks ride with the
  reactive layer (v2).

## `Std.BlueDB` app data — versioned records

For your own typed data, you own the schema. `getValue` **fails** (Task error) if
a stored value no longer decodes under the current `Codec` — a schema mismatch
surfaces instead of masquerading as a miss. Migrate on read: keep a `version`
field (or a key-prefix per version), and on a failed decode, read the raw string
(`BlueDB.get`), transform, and `putValue` the new shape. The **store file format**
itself is stable (WAL records + snapshot) — only *your record schema* migrates.

## Rollback

A build reads what the previous build wrote (gob tolerance + reset-on-mismatch),
so rolling **back** is symmetric: an older build resumes the newer sessions'
compatible fields and resets on incompatible ones. Keep migrations additive
across a deploy window if you need clean forward-and-back.
