# Autonomous goal: Sky v0.32.0 (verbatim, set 2026-11-27)

Branch `feat/let-bind-lines`, from `main` at 9d7cb1958. The previous mandate (v0.30.0) is complete.

> ok excellent, now e2e cover everything we said + migrate examples + readme doc + agents + syntax everywhere mentioned, test as well to ensure these are great additions with great imrpovement in readability + maintainability etc.
>
> please handle full release cycle e2e, in fully unattended + autonomous + PIV mode

## "Everything we said" (the agreed scope, issue #342, release v0.32.0, a minor)

- Bind line inside `let`: `p <- e`. The rest of the block and the `in` body become `andThen (\p -> rest) e`.
- `andThen` is chosen from the type of `e` (its home module's `andThen`): Task, Result, Maybe, Decoder, user types.
- Errors with fixes: `<-` outside `let`, a type with no `andThen`, an unknown type, mixed types in one block.
- `sky fmt` keeps bind lines flat. LSP hover on `<-` and a keyword semantic token.
- `Task.bestEffort`, `Task.unless` and `Task.toResult` in `Sky.Core.Task`.
- No custom operators.
- Not breaking for existing code, apart from new-name `[E1012]` notes in the migration guide.
- Migrate the examples to bind lines where it improves readability.
- Update README, docs, AGENTS.md, templates and every place that states the syntax (incl. "no do-notation" text), the docs site and sky-lang.org.
- Tests that prove the additions work and improve readability and maintainability.
- Full release cycle: release suite green, merge, tag v0.32.0, roll out to the apps (faceplan QA only).

## Follow-up mandate (verbatim, 2026-11-27, after the v0.32.0 tag)

> change them. also remember to "migrate" in the same fashion for ALL the stdlib/modules we've in sky itself, and also ALL my private apps

"Them" = the five-step `emitTick` chains in examples/30-sse-server-demo and examples/32-sse-relay.
"In the same fashion" = bind lines where they improve readability; flat one-step pipelines may stay.
Behaviour must not change (generated Go compared before and after). Private apps: the maintained list in
the private companion; faceplan stays QA only (no v* tag).
