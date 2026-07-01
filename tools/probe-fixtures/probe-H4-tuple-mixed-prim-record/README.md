# probe-H4 — `(Int, { anon })` mixed tuple

Anonymous record element inside a tuple. Pairs Cause-H widening
with the V12 anon-record cross-module identity gap; the foundation
PR-7 (defuse `globalAnonRecords` IORef) is a precondition for
this fixture to close.

**Closes by:** PR-17 + PR-7.
