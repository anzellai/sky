# NOTICE

Sky
Copyright 2025-2026 Anzel Lai

This product is licensed under the Apache License, Version 2.0 (see
[LICENSE](LICENSE)). This NOTICE file is required by clause 4(d) of
that licence; please retain it in any redistribution.

This file documents:

1. The Apache 2.0 copyright + licence summary above (required by the
   licence's NOTICE-file mechanism).
2. Prior-art attribution for parts of Sky's standard library where
   the API surface adopts conventions established by external
   projects (Std.Ui, below).
3. Per-file attribution for source files that are *derivative works*
   of code from another open-source project, along with the full
   licence text under which that upstream code was originally
   released (the elm/compiler section, below — required by
   BSD-3-Clause clauses 1 and 2).

The intent is good-faith attribution. Neither this NOTICE nor any
file in the Sky repository is a statement of endorsement by,
partnership with, or affiliation with any of the projects listed.

> **Licence history.** Sky was previously distributed under the MIT
> licence (releases up to and including v0.10.0). Existing MIT
> releases keep their original terms — that is how the grant works
> once issued. Releases from v0.10.1 onwards ship under Apache 2.0.
> See `CONTRIBUTING.md` for what this means for contributors.

---

## Std.Ui — `sky-stdlib/Std/Ui*.sky`

Sky's `Std.Ui` module provides a typed, no-CSS layout DSL for
Sky.Live applications. The public API surface (the `Element` /
`Attribute` / `Length` types; helpers like `el` / `row` / `column` /
`paragraph` / `padding` / `spacing` / `centerX` / `width` /
`alignLeft`; sub-modules `Background` / `Border` / `Font` / `Region` /
`Input` / `Keyed` / `Lazy` / `Responsive`) draws on conventions
established by the Elm community for typed layout DSLs, including:

- **mdgriffith/elm-ui** — Matthew Griffith. The Elm package that
  popularised this style of typed layout (`Element msg` /
  `Attribute msg`, named alignment helpers, `Background`/`Border`/
  `Font` sub-modules). Licence: BSD-3-Clause. See:
  <https://package.elm-lang.org/packages/mdgriffith/elm-ui/latest/>

Sky's implementation, runtime (Sky.Live VNode diff, server-side
inline-style emission, browser wire format), code generator
(typed Go output), and type system are independent work and share
no source code with the projects above. Function and type names
that overlap reflect adoption of an idiom that is now standard in
typed UI DSLs.

The full BSD-3-Clause licence under which mdgriffith/elm-ui is
released is reproduced below for completeness.

```
Copyright (c) 2020, Matthew Griffith
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions
are met:

* Redistributions of source code must retain the above copyright
  notice, this list of conditions and the following disclaimer.

* Redistributions in binary form must reproduce the above copyright
  notice, this list of conditions and the following disclaimer in
  the documentation and/or other materials provided with the
  distribution.

* Neither the name of Elm UI nor the names of its contributors may
  be used to endorse or promote products derived from this software
  without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS
FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE
COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT,
INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
(INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT,
STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED
OF THE POSSIBILITY OF SUCH DAMAGE.
```

---

## elm/compiler — adapted source files in `src/Sky/`

Several files in Sky's compiler are derivative works adapted from
the elm/compiler source tree (Copyright © 2012–present Evan
Czaplicki, BSD-3-Clause). They are kept here in modified form and
each file's header notes the upstream module it was adapted from.

The complete list of adapted files:

| Sky file                            | Adapted from                  | Relation                               |
| ---                                 | ---                           | ---                                    |
| `src/Sky/Type/UnionFind.hs`         | `Type/UnionFind.hs`           | Near-direct port; same exports + algorithm |
| `src/Sky/Type/Unify.hs`             | `Type/Unify.hs`               | Adapted (CPS-based unifier)            |
| `src/Sky/Type/Solve.hs`             | `Type/Solve.hs`               | Adapted (constraint solver loop)       |
| `src/Sky/Type/Type.hs`              | `Type/Type.hs`                | Adapted (`Variable`/`Descriptor`/`Content`/`Constraint` shape) |
| `src/Sky/Type/Occurs.hs`            | `Type/Occurs.hs`              | Adapted (occurs-check)                 |
| `src/Sky/Type/Instantiate.hs`       | `Type/Instantiate.hs`         | Adapted (scheme instantiation)         |
| `src/Sky/AST/Canonical.hs`          | `AST/Canonical.hs`            | Adapted (canonical AST shape)          |
| `src/Sky/AST/Source.hs`             | `AST/Source.hs`               | Adapted (source AST + Sky extensions)  |
| `src/Sky/Reporting/Annotation.hs`   | `Reporting/Annotation.hs`     | Adapted (region/located helpers)       |
| `src/Sky/Parse/Primitives.hs`       | `Parse/Primitives.hs`         | Inspired (uses `Text` not `ByteString`) |

For these files specifically, Sky redistributes derivative work
under the terms of the BSD-3-Clause licence below (this NOTICE
satisfies clause 1's requirement to retain the copyright notice +
list of conditions + disclaimer in source-form distribution; the
compiled `sky` binary's documentation reproduces the same in
fulfilment of clause 2).

Per clause 3 (the endorsement clause), the names "Elm" and "Evan
Czaplicki" are not used in Sky's user-facing materials to endorse
or promote Sky. Where the names appear in this repository they are
either:

- Per-file attribution required by clause 1 (in the headers of the
  adapted files listed above), or
- Factual technical references (e.g. "Sky's surface syntax is
  Elm-compatible"), which are descriptive statements about
  interoperability, not promotional comparisons.

Apart from the files listed above, the rest of Sky's compiler
(`src/Sky/Build/*`, `src/Sky/Canonicalise/*`, `src/Sky/Format/*`,
`src/Sky/Generate/*`, `src/Sky/Lsp/*`, `src/Sky/Parse/*` other than
`Primitives.hs`, `src/Sky/Sky/*`), the runtime (`runtime-go/`),
the standard library (`sky-stdlib/`), the FFI generator
(`tools/sky-ffi-inspect/`), and the CLI (`app/`) are independent
work and share no source code with elm/compiler.

The legacy bootstrap compilers in `legacy-ts-compiler/` and
`legacy-sky-compiler/` are kept in-tree as historical reference;
they are not part of the released `sky` binary and themselves do
not include elm/compiler source code.

The full BSD-3-Clause licence under which elm/compiler is released
is reproduced below for completeness.

```
Copyright 2012-present Evan Czaplicki

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions
are met:

1. Redistributions of source code must retain the above copyright
   notice, this list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above
   copyright notice, this list of conditions and the following
   disclaimer in the documentation and/or other materials provided
   with the distribution.

3. Neither the name of the copyright holder nor the names of its
   contributors may be used to endorse or promote products derived
   from this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS
FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE
COPYRIGHT HOLDERS OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT,
INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
(INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT,
STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED
OF THE POSSIBILITY OF SUCH DAMAGE.
```

---

## Sky.Live — `runtime-go/rt/live*.go`, `sky-stdlib/Std/Live.sky`

Sky.Live's architectural model — server-authoritative state, an
`init` / `update` / `view` / `subscriptions` cycle, server-side DOM
diff with patches sent over SSE, session-resident state — adopts
the now-standard "server-driven UI" approach popularised by:

- **Phoenix LiveView** — the Phoenix Framework team. The Elixir
  library that established this server-driven UI style for
  production web apps. Licence: MIT. See:
  <https://github.com/phoenixframework/phoenix_live_view>

No source code from Phoenix LiveView (or any related Elixir / Erlang
library) is included in Sky. The runtime, wire protocol, diff
algorithm, session-store implementations, and all server / browser
code are independent Go and JavaScript work. Where Sky.Live's
documentation mentions Phoenix LiveView it is for technical context
or comparison, not endorsement.

---

## Surface syntax compatibility

Sky's surface syntax (module declarations, `case`/`let`/`if`,
record literals, ADT declarations, `|>`/`<|` pipelines, type-alias
auto-constructors, exposing lists, `where`-less let-blocks) was
designed to be Elm-compatible so that Elm code can be ported with
mechanical edits. Programming-language syntax is not itself
copyrightable in the jurisdictions Sky targets (cf. *Google v.
Oracle*, 593 U.S. ___ (2021) for the analogous API/declaration
question). This statement is descriptive, not endorsement-seeking.

Sky's lexer, parser (other than `Parse/Primitives.hs`, listed
above), canonicaliser, and formatter are independent
implementations that emit and accept Elm-compatible syntax; they
share no source with elm/compiler.
