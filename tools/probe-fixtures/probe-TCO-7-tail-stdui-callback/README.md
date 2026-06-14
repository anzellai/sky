# probe-TCO-7 — TCO over `Element msg` boundary

Tail-recursive `build` over `Std.Ui.Element msg`. Exercises TCO
+ Std.Ui poly-T args together (Cause C residual). PR-23 closes
the user-ADT typed-payload long tail; PR-13 must hold the
foundation while that lands.

**Closes by:** PR-13 + PR-23.
