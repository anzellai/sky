# Modules

Modules group related declarations under a dotted name and control which names are visible to callers.

## Declaring a module

```elm
module Lib.User exposing (User, email, name, create)

import Sky.Core.Prelude exposing (..)


type alias User =
    { email : String
    , name : String
    , age : Int
    }


create : String -> String -> User
create email_ name_ =
    { email = email_, name = name_, age = 0 }


email : User -> String
email u =
    u.email


name : User -> String
name u =
    u.name
```

## The `exposing` clause

```elm
module Lib.User exposing (..)              -- everything
module Lib.User exposing (User)             -- the User type only
module Lib.User exposing (User, create)     -- User + create
module Lib.User exposing (User(..), create) -- User with all its constructors
module Lib.User exposing (User(Admin, Guest)) -- User with only some ctors
```

The canonicaliser rejects imports of unexposed names at compile time:

```
Module Lib.User does not expose `internalHelper`
```

## File layout

Module names map to file paths by replacing `.` with `/`:

- `Lib.User` → `src/Lib/User.sky`
- `Ui.Components.Button` → `src/Ui/Components/Button.sky`

Sky expects one module per file, and the filename / path must match the `module` declaration.

## Imports

```elm
import Sky.Core.Prelude exposing (..)     -- implicit in every module
import Lib.User                            -- qualified: Lib.User.create ...
import Lib.User as User                    -- aliased: User.create ...
import Lib.User exposing (User, create)    -- unqualified selected names
import Lib.User as User exposing (User)    -- both alias + exposing
```

Import resolution order:

1. The entry project's `src/`.
2. The embedded Sky stdlib (`Sky.Core.*`, `Std.*`, `Sky.Live`, `Sky.Http.*`).
3. `.skydeps/<pkg>/src/` — Sky-source dependencies installed via `sky install`.
4. `.skycache/ffi/<slug>.skyi` — FFI-generated module signatures.

## Prelude

`Sky.Core.Prelude` is implicitly imported into every module and re-exports:

- `Result(Ok/Err)`, `Maybe(Just/Nothing)` — and their combinators via `Sky.Core.Result`, `Sky.Core.Maybe`.
- `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`.
- `errorToString`.

You can still import the underlying modules explicitly to pull in more combinators (`Result.map3`, `List.foldl`, etc.).

## Cyclic imports

Not permitted. `Sky.Build.ModuleGraph.build` detects cycles and reports them.

## Sky dependencies

Sky-source packages declared under `[dependencies]` in `sky.toml` are resolved via `git clone --depth 1` into `.skydeps/<flattened-pkg-name>/` and their `src/` directories are prepended to the module graph.

```toml
[dependencies]
"github.com/anzel/sky-tailwind" = "latest"
```

After `sky install`, you can `import Github.Com.Anzel.SkyTailwind as Tailwind`.

## Visibility in practice

- Private helpers: declare them without listing them in `exposing`.
- Internal modules: prefix with `Lib.Internal.*` by convention — Sky doesn't enforce this, but humans do.
- Opaque types: expose the type name without its constructors, then export smart constructors:

```elm
module Lib.Token exposing (Token, create, value)

type Token = Token String

create : String -> Token
create s = Token s

value : Token -> String
value (Token s) = s
```
