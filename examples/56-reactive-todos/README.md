# 56 — Reactive todos (BlueDB change-feed → live UI)

A shared todo list where **every session sees writes from every other session
instantly** — no polling, no manual pub/sub. The only reactive wiring is one line:

```elm
subscriptions _ =
    Persist.watchCollection todos TodosChanged
```

When any session `Persist.insert`s a todo, the BlueDB **change-feed** publishes the
change to the collection's broker topic; every session subscribed via
`watchCollection` receives a `Change` and re-queries. `update`'s `TodosChanged`
arm just re-runs the query — the framework fans the change out to all sessions
(including the writer) so they all converge.

This exercises the reactive pipeline end to end: engine change-feed (P-R1) →
record decoder + pump (P-R2) → broker publish (P-R4a) → `Sub.subscribeTopic` →
the session's update loop → SSE repaint.

## Run

```bash
sky run src/Main.sky      # http://localhost:8000 — open two tabs, add in one
```

Add a todo in one tab; it appears in the other with no refresh.
