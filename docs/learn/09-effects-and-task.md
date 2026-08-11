# Effects & Task

Everything so far has been *pure*: given the same inputs, a function returns the
same output and touches nothing else. But real programs read files, call HTTP
APIs, query databases, ask the clock what time it is. Sky handles all of that with
one rule.

## The one rule

**Every observable side effect returns `Task Error a`.**

That gives you three tiers, and the type tells you which one you're looking at:

```elm
-- pure          — bare value:   String.length, List.map, Crypto.sha256
-- fallible-pure — Maybe:  String.toInt : String -> Maybe Int
--                 Result: Encoding.base64Decode : String -> Result Error String
-- effect        — Task Error a: File.read, Http.get, Db.query, Time.now
```

A `Task Error a` is a *description* of an effect that, when run, either produces an
`a` or fails with an `Error`. Holding a `Task` doesn't do anything yet — it's a
recipe, not the cooking.

## Sequencing effects

You build bigger tasks out of smaller ones with `Task.map` and `Task.andThen`,
exactly like `Result`:

```elm
-- read a file, then upper-case its contents
shout : Task Error String
shout =
    File.read "note.txt"
        |> Task.map String.toUpper

-- andThen: feed one task's result into the next effect
-- Task.andThen : (a -> Task e b) -> Task e a -> Task e b
```

When does a task actually run? At an *entry point* — a CLI `main`, an HTTP handler
returning its response, or `Cmd.perform` in a web app. Those are the boundaries
where Sky executes the recipe. In between, effects stay values you can pass
around, sequence, and test.

## Forcing an effect for its side effect

When you want to run a task just for what it *does* (log a line, print progress),
bind it with `let _ =` and Sky fires it:

```elm
let
    _ = println "step 1"
    _ = println "step 2"
in
keepGoing
```

## The one footgun: memoised top-level bindings

A top-level binding with no arguments is evaluated **once** and cached (it's a
constant). That's usually exactly right — a shared database pool should be opened
once and reused:

```elm
-- opened once, shared by every query — correct
db =
    Task.run (Db.connect ())
```

But it bites if you expect a *fresh* value each time:

```elm
-- ✗ freezes to ONE uuid for the whole program — colliding ids!
newId =
    Task.run Uuid.v4
```

If you need a new value per use, make it a **function** (take a unit argument),
and call it:

```elm
-- ✓ a fresh uuid every call
newId : () -> String
newId _ =
    Task.run Uuid.v4 |> Result.withDefault ""

-- call site: newId ()
```

Sky warns you when a memoised binding forces a fresh-value effect like `Uuid.v4`,
`Random.*`, or `Time.now`, so this never bites silently — but it's worth
understanding *why* the warning fires.

The same warning covers a frozen **read**: a top-level
`posts = Task.run (Store.all db postStore)` caches that row set for the life of
the process, so a post written later never appears. It fires when evaluating the
binding actually performs the read — directly, or one hop behind a helper.

It does **not** fire on a table that merely *references* handlers:

```elm
-- ✓ a registration table: nothing is read while it is built
apiRoutes =
    [ Live.api "GET /healthz" handleHealthz
    , Live.api "GET /admin/login" handleLogin
    ]
```

Passing `handleLogin` as a value stores a computation for the framework to run
per request. Its `Db.query` fires then, against the shared pool — not now, and
nothing about the list is a snapshot.

That's the whole effect story. With pure values, `Result`/`Maybe`, and `Task`, you
can express any program — and the types keep effects honest.

**[Next → Modules & imports](10-modules.md)**
