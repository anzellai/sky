# [E1015] UNKNOWN EXPORT

A module's `exposing (…)` list names something the module does not have.

```text
-- UNKNOWN EXPORT ------------------------- src/Api/Responses.sky:17:7 [E1015]

15 |     , uploadSkillZip
16 |     , decodeUploadedSkill
17 |     , decodeResponse
   |       ^^^^^^^^^^^^^^

module `Api.Responses` exposes `decodeResponse`, but does not define it.
Define `decodeResponse` in `Api.Responses`, or remove it from the
`exposing` list.
```

Added in v0.25.19.

## What it covers

| You wrote | It is an error when |
|---|---|
| `exposing (decode)` | the module has no top-level `decode = …` |
| `exposing (Foo)` | the module neither declares `Foo` nor imports it |
| `exposing (Foo(..))` | `Foo` is an alias declared in the module (an alias has no constructors to expose) |
| `exposing (Shape(Circle, Hexagon))` | `Hexagon` is not a constructor of `Shape` |

A module *can* re-expose a type it imports (`import Std.Ui.Transition exposing
(Easing)` then `exposing (Easing)`): importers reach the one original type. A
module cannot re-expose a *value* it imports, because there is no definition in
the re-exposing module to call. Write a wrapper instead:

```elm
linear : Easing
linear =
    Transition.linear
```

## Why it is an error

Before v0.25.19 a dangling export compiled. A caller's `Responses.decodeResponse
body` resolved to a definition that did not exist, the type checker accepted it
with whatever type the call site implied, and the generated Go called a `nil`
function. The program built and then panicked with `NilDereference` the first
time the call ran. That breaks "if it compiles, it works", so the export itself
is now rejected, and every use of the missing name is rejected too:

- `Responses.decodeResponse` → `[E1001]` *Undefined name*, which names the
  dangling export as the cause;
- `import Responses exposing (decodeResponse)` → `[E1011]` *not exposed*;
- `import Responses exposing (..)` then `decodeResponse` → `[E1001]`.

The editor (`sky-lsp`) reports the same diagnostics, from the same pipeline.

## How to fix it

Restore the definition, or delete the name from the `exposing` list. If other
modules still call it, they then fail with `[E1001]` at each call, which lists
the places to update.
