# fuzz regression seeds

Locked adversarial inputs for `xtask fuzz` (Property 1 — robustness). Every `.sky`
file here is fed through parse → resolve → check (→ lower when accepted) on every
fuzz run and MUST yield diagnostics, never a panic. When the fuzzer discovers a
panic-inducing input, its minimal repro is committed here so the case stays locked
even if the seeded RNG stops generating it.

These are deliberately ill-formed / degenerate programs. They are NOT part of the
`examples/` or `reject/` corpora and are never built or oracle-compared.
