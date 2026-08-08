# Typed Codegen Design

## Goal

Eliminate `any` from generated Go code. Every Sky function, list,
record, and ADT compiles to concrete Go types. The Go compiler
becomes a second type checker — if Go builds, the types are correct.

## Current state (v0.9.x)

```go
// Sky: greet : String -> String
func greet(name any) any { return rt.Concat("Hello, ", name) }

// Sky: users : List User
var users []any

// Sky: List.map toString users
rt.List_mapAny(toString, users)
```

## Target state (v1.0)

```go
// Sky: greet : String -> String
func greet(name string) string { return "Hello, " + name }

// Sky: users : List User
var users []User_R

// Sky: List.map toString users
// Monomorphised at the call site:
func List_map_User_String(fn func(User_R) string, xs []User_R) []string {
    out := make([]string, len(xs))
    for i, x := range xs { out[i] = fn(x) }
    return out
}
List_map_User_String(toString, users)
```

## Architecture

### Phase 1: Thread solved types to codegen

The HM solver already produces `SolvedTypes :: Map String Type` —
a map from binding name to inferred type. This needs to flow into
the Go emission layer.

**Files to change:**
- `src/Sky/Build/Compile.hs` — the `exprToGo` function needs access
  to the solved type of every sub-expression
- `src/Sky/Type/Type.hs` — the `Type` ADT already has all the info

**Key data flow:**
```
Parse → Canonicalise → Constrain → Solve → SolvedTypes
                                              ↓
                                         exprToGo (with types)
                                              ↓
                                         GoIr (typed)
                                              ↓
                                         Go source
```

### Phase 2: Typed function signatures

Every top-level function emits with concrete param and return types:

```go
// Before:
func update(msg any, model any) any

// After:
func update(msg Msg, model Model_R) rt.SkyTuple2[Model_R, rt.SkyTask[any, any]]
```

**Steps:**
1. For annotated functions, use the annotation directly
2. For inferred functions, use the SolvedTypes entry
3. Map Sky types to Go types:
   - `String` → `string`
   - `Int` → `int`
   - `Bool` → `bool`
   - `Float` → `float64`
   - `List a` → `[]GoType(a)`
   - `Maybe a` → `rt.SkyMaybe[GoType(a)]`
   - `Result e a` → `rt.SkyResult[GoType(e), GoType(a)]`
   - `Dict k v` → `map[string]GoType(v)`
   - `UserRecord` → `UserRecord_R`
   - `UserADT` → `UserADT` (= rt.SkyADT)
   - `a` (polymorphic) → `any`

### Phase 3: Typed lists and dicts

```go
// Before:
[]any{user1, user2}

// After:
[]User_R{user1, user2}
```

Record fields that are `List User` emit as `[]User_R` in the
struct definition. Dict fields emit as `map[string]V_R`.

### Phase 4: Typed runtime functions

Two approaches:

**A. Go generics (preferred):**
```go
func List_map[A, B any](fn func(A) B, xs []A) []B
```
The codegen emits `rt.List_map[User_R, string](fn, users)`.

**B. Monomorphisation:**
Generate a specialised function per call site:
```go
func List_map_User_String(fn func(User_R) string, xs []User_R) []string
```
More code but no generics overhead.

Go generics are sufficient — Go 1.18+ supports them. Use approach A.

### Phase 5: Typed FFI boundary

FFI returns `SkyResult[GoType(E), GoType(T)]` instead of
`SkyResult[any, any]`. The `coerceReflectArg` fallback becomes
unnecessary for well-typed code.

## Migration strategy

Incremental. Each phase can ship independently:

1. **Phase 1** first — thread types, no codegen changes yet
2. **Phase 2** — typed function signatures (biggest impact)
3. **Phase 3** — typed collections (enables gob without walkers)
4. **Phase 4** — typed runtime (eliminates reflect overhead)
5. **Phase 5** — typed FFI (eliminates coercion)

Each phase must pass: all 18 examples build, 67 self-tests,
sky-env builds, formatter idempotent.

## Non-goals for v1.0

- Higher-kinded types
- Type classes / traits
- Row polymorphism for records
- Dependent types

These are language-level features, not codegen improvements.
