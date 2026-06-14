# probe-TCO-1 — tail-recursive polymorphic accumulator

The direct PR-13 pre-mortem reproducer. `loop : Int -> List a ->
List a` self-recurses with the polymorphic `acc : List a` in
tail position. `Sky.Build.TailCallOpt` MUST emit a `continue`
block that reassigns `acc` AND `n` without coercion noise; any
deviation is a real bug.

**Pre-mortem lesson 4** applies: 2-byte diff in `continue` block
== real bug. Do NOT add to `KnownDivergence.hs`.

**Closes by:** PR-13 (parity contract).
