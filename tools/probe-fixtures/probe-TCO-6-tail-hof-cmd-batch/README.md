# probe-TCO-6 — tail call returning `Cmd.batch [...]`

The TCO continue-block carries a polymorphic `Cmd msg` arg
through `Cmd.batch`. PR-18 (Cmd/Sub kernel typed migration) is
where `Std_Cmd_batch` becomes `Std_Cmd_batch[T]`; PR-13 is the
prerequisite where the structural sigma migration must keep the
poly-T flowing through the TCO reassignment.

**Closes by:** PR-13 + PR-18.
