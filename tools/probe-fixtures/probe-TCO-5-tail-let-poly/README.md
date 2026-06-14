# probe-TCO-5 — let-bound tail-recursive `helper`

A common idiom: outer function `walk` declares an inner
`helper` via `let` and tail-calls into it. PR-12
(`lowerTypedLambda`) is where the local lambda picks up its
typed signature; PR-13 is where the TCO `continue` block
preserves that typing through reassignment.

**Closes by:** PR-12 + PR-13.
