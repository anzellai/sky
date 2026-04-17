# Sky.Live lifecycle soundness + runtime fixes

**Goal.** Sky.Live applications run correctly end-to-end. The
lifecycle (init → update → view → SSE → cleanup) is 100% sound.
Every reported bug is fixed at root cause with a regression fence.

**Done condition.** `touch .claude/skylive-lifecycle-complete`.
Stop-hook releases automatically.

If you genuinely need to pause, `touch .claude/allow-stop`.

---

## Reported bugs (fix all)

### 1. Result.traverse: "fn must be a 1-arg function"

**Symptom.** Example 06-json fails at runtime:
```
ERR: InvalidInput: Result.traverse: fn must be a 1-arg function
```
when `Result.traverse` is called with a lambda taking 2 args
(e.g. in a `List.foldl` context).

**Investigate.** `runtime-go/rt/rt.go` — find `Result_traverse`
or the kernel that implements it. Check the arity validation.
Sky's `Result.traverse : (a -> Result e b) -> List a -> Result e (List b)`
takes a 1-arg function — but curried Sky functions may present
as multi-arg at the Go level. The validation may not account for
curried functions.

**Fix.** The arity check must account for both `func(x any) any`
(direct) and `func(x, y any) any` (curried multi-arg from let
bindings or partial application). If the function is curried,
wrap it to call one arg at a time.

### 2. favicon.ico triggers full Sky.Live app init

**Symptom.** Every browser page load causes `init` to run twice
(once for the page, once for favicon.ico). Each init may hit
external services (Firestore, Stripe), wasting resources and
producing confusing double-log lines.

**Fix.** Sky.Live's HTTP handler should intercept favicon.ico
requests BEFORE session creation. Return a 204 No Content (or
serve `./public/favicon.ico` if it exists via the static handler).
This is standard web framework behaviour.

**Where.** `runtime-go/rt/` — the `ServeHTTP` or `handleRequest`
dispatcher. Add a guard before session lookup.

### 3. Skyshop "handler not found" on routes

**Symptom.** User reports hitting `/` or `/auth/signin` in browser
returns "handler not found". This may be intermittent or related
to auth state.

**Investigate.** The handlerId in the SSE event data is empty
(`"handlerId":""`). This suggests the SSE event dispatch can't
find the handler for the message. Trace the Sky.Live event
dispatch: when a browser sends an SSE event (like FirebaseAuth
callback), how does the server match it to the right handler?

**Root cause candidates:**
- The `handlerId` is empty because the frontend event binding
  doesn't set it for Firebase auth callbacks
- The session expired between page load and auth callback
- The route matching after auth redirect doesn't recognise the
  path
- Race condition: init hasn't finished but an auth event arrives

### 4. sky fmt doesn't format lists in Elm style

**Symptom.** Multi-item lists stay on one line or break incorrectly
instead of using Elm's leading-comma style:
```elm
[ item1
, item2
, item3
]
```

**Fix.** `src/Sky/Format/Format.hs` — the list formatting function
needs to break to multi-line when items exceed a threshold (or
when there are more than N items). Same for records.

### 5. Sky.Live lifecycle audit

Beyond the specific bugs above, audit the entire Sky.Live
lifecycle for soundness:

- **Session creation:** when is a session created? Can a request
  arrive before init completes? Is there a race between init and
  the first event?
- **Route matching:** how do Sky.Live routes map to browser paths?
  What happens on an unknown path?
- **SSE event dispatch:** how does an SSE event find its handler?
  What happens when handlerId is empty?
- **Session expiry:** what happens when a session expires mid-use?
  Does the user get a clear error or a broken page?
- **Concurrent requests:** is session locking correct? Can two
  requests mutate the model simultaneously?
- **Cleanup:** do sessions get cleaned up on disconnect?

---

## Verification per fix

1. `cabal test` green (including any new specs)
2. `bash scripts/example-sweep.sh --build-only` — 18/18
3. `bash scripts/example-e2e.sh` — 17/17
4. Skyshop: build + run + curl all routes → 200
5. JSON example: `cd examples/06-json && sky run src/Main.sky` —
   no Result.traverse error
6. Self-tests: 67/67

---

## Operating rules

- **No mid-session stoppage.** Push through until all fixes land.
- **Root cause fixes.** Don't mask symptoms.
- **Tests first** where practical.
- **Commit convention:** `[skylive/<tag>]` or `[fix/<tag>]`.
- **No v1.0 references.** Sky is v0.9.
