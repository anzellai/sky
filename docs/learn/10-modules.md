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

## Two imports, one name

If two imports bring in the *same* unqualified name and you use it bare, Sky
refuses to guess:

```elm
-- both modules export `label`
-- import Ambig.Alpha exposing (..)
-- import Ambig.Beta exposing (..)
-- main = println label
-- ✗ [E1012] AMBIGUOUS NAME: `label` is brought into scope by `Ambig.Alpha` and
--   `Ambig.Beta` — write `Alpha.label` or `Beta.label`
```

The reason is that the alternative is worse: picking one silently would make the
answer depend on the *order of your import lines*, so reordering imports — which
a formatter, a merge, or an added import does routinely — could change what your
program computes without a word of warning.

Three things keep this from getting in your way:

- **It only fires where you actually use the name.** Importing two modules that
  both export `title` is fine as long as you never write a bare `title`. That is
  why `import Std.Html exposing (..)` alongside `import Std.Html.Attributes
  exposing (..)` keeps working.
- **A more specific import wins.** `exposing (label)` names that one binding, so
  it beats a bulk `exposing (..)` from another module — no error, and the same
  result whichever order the two lines are in.
- **Your own definitions win, and the Prelude never competes.** A `label` you
  define in the module shadows every import, silently; and because
  `Sky.Core.Prelude` is loaded for you rather than chosen by you, an explicit
  import of your own always takes precedence over it. That is what lets
  `import Sky.Core.Math exposing (..)` redefine `abs` without complaint.

The fixes are the ones the message suggests: qualify the reference, or narrow one
import's `exposing (…)` list.

## Splitting a project

A typical web app grows into a few focused modules rather than one giant file — a
common split is `State` (your types), `Update` (the logic), `View` (the UI), and a
small `Main` that wires them together. Each imports what it needs from the others.
It keeps type-checking fast and the code navigable, and you'll see this shape in
the app-building lessons ahead.

That's the language. You now know all of Sky's surface — it really is this small.
Time to build something you can open in a browser.

**[Next → Your first web app](11-first-web-app.md)**
