# 00-standard-libs

Living regression sheet for Sky's standard library. Exercises every
public module the runtime ships with — pure modules (`String`, `List`,
`Dict`, `Maybe`, `Result`, `Math`, `Crypto`, `Encoding`, `Json.*`), the
v0.13 Layer 3 additions (`Std.Decimal`, `Std.Money`, `Std.Time`),
and the effect-typed surface (`Task`, `System`, `Log`).

```bash
sky run        # runs every assertion; exits 0 only when all pass
sky test src/Main.sky   # alternative — Sky.Test discovery picks it up
```

A failure here is **always** a regression — the modules covered are
the floor every Sky project sits on. Wire any new public stdlib
function into this sheet the same release it ships.
