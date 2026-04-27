# NOTICE

Sky is licensed under the MIT licence (see [LICENSE](LICENSE)). This file
documents prior art whose design ideas influenced parts of Sky's
standard library, alongside the licence terms of those projects.

No source code from any of the projects below is included in this
repository. Where a Sky module is *inspired by* an external library,
that influence is limited to the public API shape and naming
conventions — implementation, runtime, and codegen are independent
work written in Sky and Haskell.

This NOTICE is intended as good-faith attribution. It is not a
statement of endorsement by, partnership with, or affiliation with
any of the projects listed.

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

## Sky language — `src/`, `runtime-go/`, `app/`

Sky's syntax draws on the Elm language (Evan Czaplicki and
contributors; BSD-3-Clause). Sky is an independent compiler written
in Haskell that emits Go; it shares no source code with the Elm
compiler.

The legacy bootstrap compilers in `legacy-ts-compiler/` and
`legacy-sky-compiler/` are kept in-tree as historical reference; they
are not part of the released `sky` binary.
