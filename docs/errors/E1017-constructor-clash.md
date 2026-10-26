# [E1017] CONSTRUCTOR CLASH

A bare constructor could mean two different constructors, and an import picked
one of them without saying so.

```text
-- CONSTRUCTOR CLASH ------------------------------- src/Main.sky:12:5 [E1017]

12 |     Error E.Io { message = "x", details = Nothing }
   |     ^^^^^ this `Error` has 2 meanings

The constructor `Error` here could mean the `Error` from `Sky.Config` or
`Sky.Core.Error` (the Prelude's `Error`). The import of `Sky.Config` puts its
`Error` in front of the other, so this reference would silently mean
`Sky.Config.Error`, and a program that meant the other one fails later with an
error that never names the import. Sky rejects it instead of picking one.
Qualify the reference: write `Config.Error` for the one from `Sky.Config` or
`E.Error` for the one from `Sky.Core.Error`. An import whose `Error` is never
written unqualified needs no change.
```

Added in v0.27.18.

## What it covers

| You wrote | It is an error because |
|---|---|
| `import Sky.Config as Config exposing (LogLevel(..))`, then a bare `Error` | `LogLevel` has an `Error` constructor, and `Error` is also the Prelude's `Error` |
| `import Lib.A exposing (Colour(..))` and `import Lib.B exposing (..)`, both with a `Red` constructor, then a bare `Red` | the named import wins over the `(..)` import with no message |

It fires in an expression (`Error kind info`) and in a pattern (`Error ->`).

## What it does not cover

- An import that brings the clashing constructor but never uses it bare. A
  module that imports `exposing (LogLevel(..))` and uses only `Info` compiles.
- A qualified reference: `Config.Error`, `E.Error`, `A.Red`.
- A constructor your own module defines. It wins over an import, as before.
- `Just`, `Nothing`, `Ok`, `Err`, `True` and `False`. Importing
  `Maybe(..)` or `Result(..)` names the same constructors the Prelude has.
- Two imports of the same kind (`exposing (..)` twice) that bind one name.
  That is `[E1012]` AMBIGUOUS NAME, as before (`docs/rust-rewrite/05-name-resolution.md` §6b).

## Why it is an error

Before v0.27.18 the import won silently. A program written for the Prelude's
`Error` built a `LogLevel` instead, and the error the user saw was
`type mismatch: LogLevel vs ErrorKind -> _`, which says nothing about the
import that caused it. In a pattern the wrong constructor picks the wrong
branch. Elm reports a constructor exposed twice as ambiguous.

## How to fix it

Qualify the reference:

```elm
import Sky.Config as Config exposing (LogLevel(..))
import Sky.Core.Error as E


level : LogLevel
level =
    Config.Error


failure : E.Error
failure =
    E.Error E.Io { message = "disk full", details = Nothing }
```

A pattern takes the same qualified form: `Config.Error ->`.
