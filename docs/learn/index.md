# Welcome to the Sky tour

Sky is a **pure-functional, Elm-family language that compiles to typed Go**. One
language for the whole stack — web apps, APIs, CLIs, terminal UIs, desktop — with
a single promise: **if it compiles, it works.** No null, no user-written FFI, no
runtime panics from well-typed code.

This tour teaches Sky from scratch. Each lesson is short: one idea, a small
example, and a note or two. Work through it in order — every lesson builds on the
one before. You can read it without installing anything, but you'll learn more if
you follow along in a real project (the next lesson sets that up in a minute).

## How the tour is laid out

- **Start** — install Sky and build your first program.
- **The language** — values, functions, records, unions, lists, `Maybe`/`Result`,
  pipelines, effects, modules. This is all of Sky's surface; it's small.
- **Building apps** — your first web app with Sky.Live, UI, forms, routing, data,
  auth, and shipping it.
- **Next steps** — a chapter for developers **[coming from another
  language](18-coming-from-other-languages.md)**, and how to **[use AI
  tools](19-ai-tooling.md)** to write Sky.

When you want to look something up rather than learn in order, the
[API reference](../reference.html) has every standard-library module (searchable),
and the [guides](../guide/index.html) go deep on each topic.

## The shape of a Sky program

Here's a whole program. Don't worry about the details yet — just notice the
shape: a module header, some imports, a type, a function, and `main`.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

type Msg = Increment | Decrement

update : Msg -> Int -> Int
update msg count =
    case msg of
        Increment -> count + 1
        Decrement -> count - 1

main =
    println (String.fromInt (update Increment 0))
```

If you've written Elm, this is home. If you haven't, the next few lessons walk
through every piece. Ready?

**[Start → Your first app](01-first-app.md)**
