# probe-J-policy-rename

Exercises **Cause J** — safeReturnType policy split.

C7 renamed `safeReturnType` → `safeReturnTypeFull` (and
`safeReturnTypeWith` → `safeReturnTypeBootstrap`). This fixture
locks the post-rename behavior on a typed-record-alias return that
exercises the full-env path.

`mkPerson : String -> Int -> Person` must lower with typed return
`Person_R` (via safeReturnTypeFull's record-alias narrowing).
If C7's rename ever regresses to bare `any` returns, this fixture
catches it at the next probe-sweep run.

**Current state: GREEN** post-C7.
