# probe-H2 — `(Cfg msg, Model)` tuple

The Cause-H Step-4 headline that broke three smart-PR-4 attempts.
Non-primitive elements force the tuple aliasing to widen, but the
foundation gap (`mapSkyTypeToGo` mirrors env-free `typeToGo`, not
the env-aware `solvedTypeToGo`) means user-record types resolve as
`any` even after the Step-4 gate flips.

**Closes by:** PR-17 (Ship Point B), gated on PR-5
(`Sky.Type.Solve.GoTypeBuild`) being green at parity first.

This is the probe that empties on Ship Point B exactly; if it
doesn't, foundation work hasn't actually closed.
