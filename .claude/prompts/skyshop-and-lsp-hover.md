# Skyshop runtime + LSP hover completeness

**Goal.** Two non-negotiable outcomes:
1. **Skyshop example runs correctly end-to-end.** Every route
   responds with the right content, no "handler not found", no
   double-init on favicon, styling renders properly. If the user
   can see a regression, it's not done.
2. **LSP hover shows correct type signatures for ALL identifiers.**
   Every variable, function, constructor, imported name, stdlib
   kernel — if the cursor is on it, hover returns its type. "Shows
   the variable name" is unacceptable; the type must be there.

**Done condition.** `touch .claude/skyshop-lsp-hover-complete`.
Stop-hook releases automatically.

If you genuinely need to pause, `touch .claude/allow-stop`.

---

## Part A — Skyshop runtime correctness

### Symptoms reported

1. Hitting `/` shows "handler not found" (intermittent — may be
   stale binary / port squatter; reproduce first)
2. Double init: `[DB] Firestore client initialised` and `[APP]
   SkyShop initialised` appear twice in stderr
3. Styling is messed up
4. `/auth/signin` after Google sign-in renders "handler not found"

### Diagnostic methodology

1. **Clean build from scratch:**
   ```bash
   cd examples/13-skyshop
   rm -rf sky-out .skycache .skydeps
   sky build src/Main.sky
   ```
2. **Kill any port-8000 squatter before running:**
   ```bash
   lsof -ti:8000 | xargs kill 2>/dev/null; sleep 0.3
   ./sky-out/app
   ```
3. **Verify every route the app declares** (check `main` in
   `src/Main.sky` for the `routes` list). Each route must return
   HTTP 200 with expected content. Specifically:
   - `GET /` — home page with product listings
   - `GET /auth/signin` — sign-in page with Google OAuth
   - `GET /cart` — cart page
   - `GET /orders` — orders page
   - `GET /admin` — admin page
   - `GET /privacy` — privacy policy
   - `GET /terms` — terms page
4. **Check styling:** does the HTML contain the Tailwind CSS
   classes? Is the `<style>` block present in the initial render?
5. **Double init:** investigate whether this is favicon.ico
   creating a second Sky.Live session. If so, add a favicon route
   or static handler that serves a 204 without init. If it's a
   real bug (init running twice per request), fix at the root.

### Fix rules

- Fix at the code level — the example or the runtime, whichever
  is at fault. Don't mask issues.
- If the issue is in the Sky.Live runtime (`runtime-go/rt/`), fix
  it there, not in the example.
- After each fix, rebuild + retest ALL routes, not just the fixed one.
- Run `bash scripts/example-e2e.sh` to verify e2e contracts.

---

## Part B — LSP hover shows type signatures for ALL identifiers

### Current state

- LSP hover works for some identifiers but returns just the name
  (no type) for many variables, functions, and stdlib references.
- The root cause is in `src/Sky/Lsp/Server.hs` — the hover handler
  (`computeHover` or equivalent) doesn't have access to solved types
  for all bindings, or the type lookup is too narrow.

### What "complete" means

For **every** identifier the cursor can land on:

| Category | Example | Expected hover |
|---|---|---|
| Top-level annotated fn | `greet : String -> String` | `greet : String -> String` |
| Top-level inferred fn | `helper x = x + 1` | `helper : Int -> Int` (or `number -> number`) |
| Local let binding | `let x = 42 in ...` | `x : Int` |
| Lambda param | `\name -> ...` | `name : a` (or inferred) |
| Case-bound var | `Ok val -> val` | `val : a` (or the concrete type) |
| ADT constructor | `Just` | `Just : a -> Maybe a` |
| Imported function | `String.length` | `String.length : String -> Int` |
| Stdlib kernel fn | `println` | `println : String -> Task Error ()` |
| FFI function | `Mux.routerHandleFunc` | The skyi signature |
| Record field access | `user.name` | The field type |

### Diagnostic methodology

1. Start `sky lsp` manually, feed it a fixture with didOpen,
   hover on various positions, and log what comes back.
2. Use the existing test harness (`test/Sky/Lsp/Harness.hs`) to
   write hover specs for each category above.
3. Read `src/Sky/Lsp/Server.hs` — find `computeHover` or the
   hover handler. Trace what symbol lookup it does and where types
   come from.

### Fix strategy

The LSP needs the **solved type environment** to answer hover
queries with types. Currently it may only have the parsed/
canonicalised AST (which has names but not inferred types). The
fix likely involves:

1. Running the full pipeline (Parse → Canonicalise → Constrain →
   Solve) on didOpen / didChange, just as `runPipeline` does for
   diagnostics.
2. Storing the solved types (the `SolvedTypes` map from
   `Solve.solveWithTypes`) in the document state alongside the
   parsed AST.
3. Using the solved types in the hover handler to look up the
   type of any identifier at a given position.

For stdlib kernels and FFI functions, the types come from the
kernel function registry and `.skyi` files respectively. The
hover handler needs to consult these sources too.

### Fix rules

- **Test first.** Add hover specs in `test/Sky/Lsp/` for each
  category in the table above. Each spec should FAIL at HEAD,
  then pass after the fix.
- **Don't break existing hover.** The annotated-function hover
  already works — don't regress it.
- **Types must be human-readable.** Use the pretty-printer
  (`formatScheme` / `formatTypePairForError`) so users see
  `a -> b -> a`, not `t108 -> t204 -> t108`.
- **Position-accurate.** Hover must work on the exact character
  position, not just "anywhere on the line".

---

## Part C — CI parity

- `sky verify` stale-file bug is fixed (commit f1b278e). After
  all Part A + B work, push and watch CI green on both platforms.
- If CI still fails, debug at root cause — no "it works locally"
  hand-waving.

---

## Verification per item

1. All 18 examples build: `bash scripts/example-sweep.sh --build-only`
2. Skyshop routes verified: curl each route, assert content
3. E2E contracts: `bash scripts/example-e2e.sh` green
4. Cabal test: `cabal test` green (including new hover specs)
5. Self-tests: 67/67
6. LSP hover specs: one per category in the table, all green
7. CI: push, both platforms green

---

## Operating rules

- **No mid-session stoppage.** The user explicitly said "no excuse
  for mid-session stoppage or out of scope or unrelated bug." If
  you hit a wall, push through or document exactly what's blocking
  and propose the minimal unblock path.
- **No "pre-existing" dismissals.** If the user sees a bug, fix it.
  Classification as "pre-existing" is not an excuse to skip it.
- **No v1.0 references.** Sky is v0.9.
- **No Result→Task FFI changes.** Settled design.
- **Tests first.** Every fix has a regression fence.
- **Commit convention:** `[skyshop/<tag>]` or `[lsp/hover-<tag>]`.
