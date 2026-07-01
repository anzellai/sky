# probe-TCO-3 — mutual recursion over `List a`

`parityEven` ↔ `parityOdd` mutually recurse over `List a`. Sky's
TCO rewriter only handles self-recursion, so these compile as
ordinary recursive Go functions. The probe asserts the
polymorphic `xs : List a` arg STAYS structurally typed through
the value-side seam migration — collapsing to `any` here means
every call site cons-pattern destructure routes through reflect.

**Closes by:** PR-13 (parity).
