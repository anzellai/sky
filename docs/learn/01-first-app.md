# Your first app

Let's get Sky running and build something in under a minute.

## Install

Grab the latest release binary and put it on your `PATH` (the project README has
the platform-specific command). Check it works:

```
sky --version
```

Once installed, `sky upgrade` keeps it current.

## Create a project

```
sky init hello
cd hello
```

`sky init` scaffolds a tiny project: a `sky.toml` (project config), a
`src/Main.sky` (your code), and an `AGENTS.md` + `CLAUDE.md` so any AI assistant
already knows how to write Sky here. The generated `src/Main.sky` looks like:

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println "Hello, Sky!"
```

## Build and run

```
sky run src/Main.sky
```

You'll see the phased pipeline log (parse → canonicalise → type → lower → Go
build) and then:

```
Hello, Sky!
```

`sky run` builds **and** runs. To just compile, use `sky build src/Main.sky` (it
produces `sky-out/app`). To type-check without running, `sky check` — and in Sky,
`sky check` is exactly `sky build` minus running the binary: both compile the
generated Go, so a green check means it really builds.

## The edit loop

While developing, keep a watcher running:

```
sky watch src/Main.sky
```

It rebuilds and restarts on every save. A warm rebuild is a second or two.

## Change something

Edit `main` to do a little work:

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

main =
    println (String.fromInt (2 + 3 * 4))
```

Save, and the watcher prints `14`. `String.fromInt` turns the `Int` into a
`String` so `println` can show it — Sky won't silently convert types for you, and
that's a feature we'll come back to.

## What just happened

Sky compiled your `.sky` source to Go, then compiled that Go to a native binary.
You get Go's deployment story — one static binary, no runtime to install — from a
language with no null and no exceptions. The rest of the tour is about the
language you'll write to fill that binary.

**[Next → Values & types](02-values-and-types.md)**
