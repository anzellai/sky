# 58 — Reactive todos, FULLY DECLARATIVE (`Persist.live`)

The same shared todo list as example 56, but with **zero reactive code**. Compare
the two `update` functions: 56 has a `TodosChanged` Msg + a re-query + coalescing;
this one has **none of that**. The whole feature is two lines:

```elm
reactiveQueries model =
    [ Persist.live db (Persist.query todos |> Persist.orderAsc "id")
        (\rows m -> { m | todos = rows }) ]

main = Live.app (config { … } |> Live.withReactive reactiveQueries)
```

You declare a query and where its result goes in the Model. The framework keeps it
fresh forever — runs it at mount, and re-runs + folds it in whenever any session's
write could change it. No `subscriptions`, no Msg, no re-query, no polling.

Works on every backend (KV + SQLite + Postgres). For custom control (merge a change
instead of re-query), drop to `Persist.watchCollection` (example 56).

Two-session e2e: `REACTIVE_PORT=8000 node scripts/verify-reactive-todos.mjs`
(browser-verified, ~68ms).
