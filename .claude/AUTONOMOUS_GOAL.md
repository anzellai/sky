# Autonomous goal: Sky v0.29.14

Branch `fix/v0.29.14`, from `main` at fc2f7437e (v0.29.13 is tagged and
published). Private context: `.claude/AUTONOMOUS_GOAL.private.md` (gitignored).
The previous mandate (v0.29.1) is complete.

## The user's words (2026-11-17)

"there are defects discovered by working on a private project, take the notes
of these, and investigate + then research + analyse + plan on fixes if needed.
if all of them are worth fixing/updating, 100% unattended + autonomous + PIV
mode until e2e validated + completed. then tag release as next patch release."

## The notes (verbatim headings; the full text is in the private companion)

Part A. Defects (found on v0.29.12 in a large multi-module app):

- D1. A second entry builds the wrong backend, silently.
- D2. Alias of an alias becomes a recursive alias in `Shared`.
- D3. Name clash in the generated `Shared`.
- D4. The split creates an import cycle in the client tree.
- D5. The client copy of a mixed module keeps `update` but drops its server
  helpers.
- D6 (minor). The follow-up write is lost for a request without `X-Sky-Wire`.
- D7. A user type named `Route` clashes with `App.Route` in the generated Go.
- D8. Sky.Live `html` patch on a table loses its structure.

Part B. Runtime features (after part A):

- B1. Server admission control for `/_rpc` and `App.api` routes.
- B2. A deadline that travels with a Task (`Task.timeout ms task`).
- B3. A circuit breaker in the stdlib for ports.
- B4. `Sub.viewport` and a query-string reader.
- B5. Coverage (`//line` directives, `sky test --coverage`,
  `sky coverage report`).

Per item: a failing reproduction as a test first, the fix, existing suites
green, a line in `docs/migration/v0.29.md` and `CHANGELOG.md`. One commit per
item, Conventional Commits title. No outside project named anywhere.

Gate: every reproduction is a test, the full Sky test suite passes, `sky
verify` passes on the example projects, then the full release workflow, then
tag v0.29.14.

## Found during the work (ship in this release, CLAUDE.md §0.2.2)

- F1. An `App.web` app built with `--target web:app` answers a plain 404 for
  a declared route other than `/`.
- F2. An undefined type name inside a record alias gives a type mismatch,
  not an undefined-name error.
- F3. A failed backend leg prints a "Broken pipe" wasm-name handoff message
  that hides the real error.
- F4. `httpRequestDo` does not attach the request context, so an HTTP client
  call carries no trace and no cancellation (fixed with B2).

## Decisions

- B5: a spike (2026-11-17, go1.27.3) shows `go build -cover` ignores `//line`
  directives (the profile keeps main.go lines). B5 maps coverage through a
  side map from main.go line ranges to Sky module, function and case arm.
- B1 ships with no default limit. The adaptive limit is cut.
- F5. `scripts/build.sh` installed the compiler with `cp -f`, and macOS
  killed the new binary (stale code signature). Fixed in 9d3db3af9.
