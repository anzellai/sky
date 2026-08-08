# Modules & imports

Every Sky file is a module. As a project grows you split it across several — and
the module boundary is enforced, not just convention.

## The header

The first line names the module and lists what it lets others use:

```elm
module Cart exposing (Item, total)
```

`Cart` exposes the `Item` type and the `total` function. Anything declared but
left off that list is **private** to the module — a consumer simply can't reach
it. The `exposing` list is a real contract.

## Importing

```elm
import Sky.Core.List as List exposing (map, filter)
import Std.Log exposing (println)
```

- `import M` brings the module in; you call its functions qualified: `List.map`.
- `as Alias` renames the qualifier: `import Std.Db as Db` → `Db.query`.
- `exposing (map, filter)` also brings those names in *unqualified*, so you can
  write `map` directly.

Even without an alias, the last segment of a module name auto-qualifies:
`import Sky.Core.Prelude exposing (..)` lets you write both the exposed names and
`Prelude.identity`.

## The boundary is enforced

If you try to import a name a module doesn't expose, that's a hard error:

```elm
-- module Cart exposes (Item, total) but NOT `secretDiscount`
-- import Cart exposing (secretDiscount)
-- ✗ [E1011] NOT EXPOSED: module `Cart` does not expose `secretDiscount`
```

This holds for the standard library too — the export list means what it says.

## Splitting a project

A typical web app grows into a few focused modules rather than one giant file — a
common split is `State` (your types), `Update` (the logic), `View` (the UI), and a
small `Main` that wires them together. Each imports what it needs from the others.
It keeps type-checking fast and the code navigable, and you'll see this shape in
the app-building lessons ahead.

That's the language. You now know all of Sky's surface — it really is this small.
Time to build something you can open in a browser.

**[Next → Your first web app](11-first-web-app.md)**
