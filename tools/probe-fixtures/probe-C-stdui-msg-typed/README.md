# probe-C-stdui-msg-typed

Exercises **Cause C** — Std.Ui kernel-sig polymorphism erased.

`view : Int -> Element Msg` constructs UI with typed callbacks
(`Ui.button { onPress = Just Increment }`).  Today the Std.Ui
kernel sigs are non-generic so Msg flows through `func(any) any`
HOFs.  C13-runtime + C14 close this by introducing
`type Std_Ui_Element[T1 any] = rt.SkyADT` and typed call-site
instantiation.

**Current state: GREEN** (view compiles + types-flow far enough).
**Tighten when C13/C14 lands** — assert
`MUST_CONTAIN "Std_Ui_Element[Msg]"` (typed Element return) and
`MUST_CONTAIN "Std_Ui_button[Msg]"` (typed kernel instantiation).
