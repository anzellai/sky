# probe-TCO-2 — tail call with `(Cfg msg, State)` tuple

TCO + Cause-H combined. The continue-block reassignment for a
typed tuple has to flow the structural type AT PR-13, even
though the typed-tuple emission itself only lands at PR-16/17.
This is the multi-PR interaction the audit missed.

**Closes by:** PR-13 (continue-block parity) + PR-16 (typed
tuple destructure).
