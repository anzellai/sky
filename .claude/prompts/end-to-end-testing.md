# End-to-end example test harness

**Problem.** Our current CI catches compile-time regressions (cabal
test, example sweep) and smoke-level runtime panics (`sky verify`
probes `/`). It misses three real failure classes:

1. **Logic regression** — code compiles and runs, no panic, HTTP 200,
   but the app's domain behaviour is wrong. Example: `16-skychess`
   AI making nonsense moves after a compiler/runtime change that
   altered evaluation or Move ordering.

2. **CLI flow regression** — `sky verify` only probes HTTP servers.
   CLI examples get a pass for "exits 0" but not for "actually does
   what the subcommand says". Example: `07-todo-cli add "buy milk"`
   appears to succeed but doesn't persist, or `list` returns empty.

3. **DB state regression** — endpoints return 200 but database
   operations half-succeed (insert + unique constraint error,
   orphaned rows, broken foreign keys). Example: `12-skyvote`
   comment insertion returning an error after the row landed.

User had to catch all three manually. That's what this prompt
exists to prevent.

**Goal.** Build a `sky verify e2e` subcommand (or a companion
`scripts/example-e2e.sh`) that runs each example against a
behavioural contract, not just a smoke probe. Do not stop until:

- Every example has an e2e contract file
- The harness runs all contracts and reports per-example pass/fail
- CI invokes the harness after `sky verify`
- The three bugs the user just caught have regression tests that
  FAIL against HEAD~1 and pass at HEAD (once fixed)

If you need to pause, `touch .claude/allow-stop`.

---

## Design

### Contract file: `examples/<n>/e2e.json`

Each example declares its behavioural contract in a JSON file
alongside `verify.json`. Three variants based on example kind:

**CLI examples** (`01-hello-world`, `02-go-stdlib`, `06-json`,
`07-todo-cli`, `14-task-demo`):

```json
{
    "kind": "cli",
    "steps": [
        {
            "args": ["add", "Buy milk"],
            "expectExit": 0,
            "expectStdoutContains": ["Added"]
        },
        {
            "args": ["add", "Write docs"],
            "expectExit": 0
        },
        {
            "args": ["list"],
            "expectExit": 0,
            "expectStdoutContains": ["Buy milk", "Write docs"]
        },
        {
            "args": ["done", "1"],
            "expectExit": 0
        },
        {
            "args": ["list"],
            "expectExit": 0,
            "expectStdoutContains": ["[x] Buy milk", "[ ] Write docs"]
        }
    ],
    "setup": { "cleanArtifacts": ["todos.db"] }
}
```

**HTTP server examples** (`05-mux-server`, `08-notes-app`,
`15-http-server`, `18-job-queue`):

```json
{
    "kind": "server",
    "port": 8000,
    "startupWaitMs": 2000,
    "steps": [
        {
            "method": "POST",
            "path": "/auth/sign-up",
            "form": { "email": "test@example.com", "password": "correct-horse-battery" },
            "expectStatus": 200,
            "expectBodyContains": ["Welcome"],
            "captureCookies": ["session"]
        },
        {
            "method": "POST",
            "path": "/notes/new",
            "form": { "title": "First note", "body": "Hello" },
            "expectStatus": 302,
            "sendCookies": ["session"]
        },
        {
            "method": "GET",
            "path": "/notes",
            "expectStatus": 200,
            "expectBodyContains": ["First note"],
            "sendCookies": ["session"]
        }
    ]
}
```

**Sky.Live examples** (`09-live-counter`, `10-live-component`,
`12-skyvote`, `13-skyshop`, `16-skychess`, `17-skymon`):

```json
{
    "kind": "live",
    "port": 8000,
    "startupWaitMs": 2000,
    "steps": [
        {
            "name": "initial render",
            "method": "GET",
            "path": "/",
            "expectStatus": 200,
            "expectBodyContains": ["<button", "onClick"],
            "captureCookies": ["sky_sid"]
        },
        {
            "name": "dispatch Increment",
            "method": "POST",
            "path": "/_sky/event",
            "jsonBody": { "sessionId": "$sky_sid", "msg": "Increment", "args": [] },
            "expectStatus": 200,
            "expectJsonFieldContains": { "patches": "count: 1" }
        }
    ]
}
```

### Harness: `scripts/example-e2e.sh`

Bash driver that:

1. Builds each example via `sky build src/Main.sky`.
2. Reads `e2e.json`.
3. For `cli`: resets artefacts, runs each step as `./sky-out/app <args>`, compares stdout/exit against expectations.
4. For `server` / `live`: starts the binary, waits `startupWaitMs`, runs each step via curl (with cookie jar for session persistence), kills the process.
5. Aggregates per-example pass/fail, non-zero exit on any failure.

Alternative: extend `sky verify` in Haskell (`app/Main.hs` or
`src/Sky/Build/Compile.hs`) to read `e2e.json` and run the same
loop. Reuses the scenario-runner code path from audit P2-4. Pick
whichever matches the codebase style better — shell script is
faster to write, Haskell gives better error reporting and
integration with the existing verify subcommand.

**Recommendation:** extend the Haskell `sky verify` path. Adds a
new `--e2e` flag (or makes e2e the default when `e2e.json`
exists). Reuses the same verify.json scenario runner's HTTP code,
adds a CLI branch, and reports with the same formatting as
existing verify output.

### CI wiring

`.github/workflows/ci.yml` — after the existing `sky verify`
step, add:

```yaml
- name: Run sky verify --e2e (behavioural contracts)
  run: sky verify --e2e
```

If any e2e contract fails, CI goes red. Before landing, the user
should be able to see which example + which step + what mismatched
(expected vs actual).

---

## Items (work top-to-bottom)

### Item 1: Write e2e contract files for every example

18 examples + `simple` + `test_pkg` = 20 directories. Each needs
an `e2e.json`. Start with the ones the user hit:

- **`07-todo-cli/e2e.json`** — add → list → done → list flow
- **`16-skychess/e2e.json`** — start game → make a move → verify
  AI responds with a non-pathological move (at minimum: not
  self-check, captures a hanging piece when available)
- **`12-skyvote/e2e.json`** — sign-up → sign-in → create idea →
  comment on idea → verify comment shows in idea detail WITHOUT
  a DB error in the logs

Then fill in the rest. Keep contracts minimal but real —
one good path + one failure path per example where feasible.

For Sky.Live examples, the event-dispatch path requires constructing
JSON bodies matching the client's wire format (see
`runtime-go/rt/live.go:handleEvent` for the schema). Extract this
into a helper or document the format in the prompt so subsequent
contract authors don't re-derive it.

### Item 2: Extend `sky verify` with e2e runner

`app/Main.hs` (or wherever `verify` is wired):

- Accept `--e2e` flag OR auto-activate when `examples/<n>/e2e.json` exists.
- For each example: parse the JSON, dispatch to `runCliContract`,
  `runServerContract`, or `runLiveContract` based on `kind`.
- Use `http-client` or existing process-based curl invocation
  (whichever is already in the codebase — VerifyScenarioSpec
  uses `readCreateProcessWithExitCode (shell ...) ""` so shell
  curl is precedent).
- Report each step's pass/fail with the same output style as the
  existing verify runner (`runtime ok: X (e2e: 5 steps)` /
  `FAIL e2e: X step Y: expected ... got ...`).

For CLI contracts, each step is `readCreateProcessWithExitCode
(proc "./sky-out/app" args) ""`. Compare exit code + stdout
substring + stderr substring.

For server/live contracts, start the binary as a subprocess,
`threadDelay startupWaitMs * 1000`, run each step via curl with
`-b cookies.txt -c cookies.txt`, terminate the process after.
Retain stderr in a file so panic messages surface in the failure
report.

### Item 3: Regression tests for the three known failures

Before declaring e2e complete, the harness must surface the three
bugs the user just caught:

#### 3a. todo-cli args
`07-todo-cli/e2e.json` steps:
```json
{"args": ["add", "First task"], "expectStdoutContains": ["Added"]},
{"args": ["list"], "expectStdoutContains": ["First task"]}
```
Current HEAD: `add` apparently doesn't persist, `list` shows nothing.
Harness should FAIL.

Then debug and fix — likely a `sky.toml` env→code path bug, or
Os.args handling regression since the Os module has been churning.
The fix lands in a separate commit; the contract stays as the
regression fence.

#### 3b. skychess AI quality
Pure "AI doesn't panic" isn't enough. Add:
```json
{
    "name": "AI plays a legal move",
    "method": "POST",
    "path": "/_sky/event",
    "jsonBody": {"sessionId": "$sky_sid", "msg": "ClickSquare", "args": [12]},
    "expectStatus": 200
},
{
    "name": "AI move was legal (captures hanging queen)",
    "method": "GET",
    "path": "/",
    "expectBodyContains": ["position after black reply"],
    "pollIntervalMs": 600,
    "pollMaxAttempts": 3
}
```
Contract should verify at minimum that the AI makes a pseudo-legal
move and doesn't play an obviously-bad move in a position where a
free capture is available. If the AI eval regressed, the harness
FAILs.

For "AI quality" the bar is pragmatic: run the opening from a
fixed position where Black has one obvious capture, assert the
AI takes it. Not a full regression-to-old-Elo but enough to catch
"evaluation returns 0 for everything" regressions.

#### 3c. skyvote comment unique-constraint
`12-skyvote/e2e.json`:
```json
{
    "name": "add a comment",
    "method": "POST",
    "path": "/_sky/event",
    "jsonBody": {"sessionId": "$sky_sid", "msg": "SubmitComment", "args": ["idea-123", "Nice idea"]},
    "expectStatus": 200,
    "expectJsonFieldContains": {"patches": "Nice idea"}
},
{
    "name": "reloading the idea page shows the comment AND no DB error log",
    "method": "GET",
    "path": "/idea/idea-123",
    "expectStatus": 200,
    "expectBodyContains": ["Nice idea"],
    "expectStderrAbsent": ["UNIQUE constraint failed", "ERROR", "panic"]
}
```

`expectStderrAbsent` is new but load-bearing — for DB state bugs
that return a valid response but log a DB error, we need to
inspect the server's stderr. The harness keeps a file handle on
the process's stderr and greps it after each step.

### Item 4: Wire CI

Update `.github/workflows/ci.yml` to run `sky verify --e2e` after
`sky verify`. All e2e contracts must pass for CI to be green.

### Item 5: Verification

Before commit:
- `bash scripts/example-sweep.sh --build-only` → 18/18
- `sky verify` (existing behaviour) → all runtime ok
- `sky verify --e2e` → all contracts pass (after fixing the three
  known bugs)
- `cabal test` → green
- `go test ./rt/` → green
- The three known bugs are FIXED in separate commits, each with
  the e2e contract as the regression fence

### Item 6: Commit structure

Commit topology:
1. `[e2e] harness: add e2e.json schema + sky verify --e2e runner`
   — just the harness, no contracts yet. Doesn't change behaviour
   of any example.
2. `[e2e] contracts for CLI examples (01, 02, 06, 07, 14)`
   — initial contracts. todo-cli contract FAILS; flag in commit
   message.
3. `[e2e] fix 07-todo-cli: <root cause>` — fixes the bug; contract
   now green.
4. Repeat for skychess and skyvote.
5. `[e2e] contracts for remaining examples (05, 08, 09, ..., 18)`
6. `[ci] wire sky verify --e2e into workflow`

Each commit should keep the entire test matrix passing (or
explicitly fail on the commit that introduces a contract for an
existing bug, with the next commit being the fix).

---

## Operating rules

- **Real behaviour, not proxy.** Don't write contracts that just
  check HTTP 200 — that's already in `sky verify`. Each step must
  assert something about the app's actual domain output (counter
  incremented, note persisted, comment rendered, chess move
  reasonable).
- **No flaky tests.** Polling or sleep-based waits must have
  bounded retries and clear failure messages. A flaky e2e test is
  worse than no test — it trains the team to ignore red CI.
- **Capture everything on failure.** When a step fails, the
  harness should dump the last N lines of the process's stderr,
  the last HTTP response body, and the full curl command so the
  user can re-run the step manually.
- **Don't change example semantics to pass tests.** If an example's
  domain behaviour is wrong, the FIX is a domain-code change, not
  a contract relaxation.
- **v0.9 — don't add v1.0 references.** Same rule as prior prompts.

## Done condition

- `scripts/example-e2e.sh` exists OR `sky verify --e2e` works
  (pick one; recommend the latter)
- Every example has `e2e.json`
- `sky verify --e2e` exits 0 against current HEAD
- `.github/workflows/ci.yml` runs the e2e step
- The three bugs the user just reported (todo-cli, skychess,
  skyvote) are fixed, each with its regression contract in place
- `test/Sky/Build/VerifyE2eSpec.hs` (or similar) wraps a single
  example's e2e flow as a cabal-test so `cabal test` surfaces
  e2e regressions too

Stop-hook releases automatically when the marker file
`.claude/e2e-harness-complete` exists (touch it at the end of
item 6).
