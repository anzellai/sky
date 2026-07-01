# Case-emitter cross-ADT pollution — diagnosis

**Status**: open (iter 68 — diagnosis only; fix deferred to iter 69+)
**Surfaced by**: v0.17 iter 68 attempt to flip `Std.Ui.Transition.Easing`
**Branch state at observation**: `feat/v0.17-fully-typed-codegen @ 5524afc0` plus a temporary local Easing-flip patch

## Symptom

With the sealed-iface allowlist set to

```haskell
sealedIfaceFlipAllowList = Set.fromList
    [ "Sky.Test.TestResult"
    , "Sky.Core.Jwt.Algorithm"
    , "Std.Ui.Animation.Iterations"
    , "Std.Ui.Transition.Easing"   -- iter 68's attempted 4th flip
    ]
```

`examples/26-ui-showcase` builds clean through Sky lowering, then `go build` fails:

```
./main.go:2833:7: undefined: Std_Ui_Transition_Easing_Fr_V
```

The emitted Go shows a `Std_Ui_Grid_trackToCss` function whose
type-switch over a `Std_Ui_Grid_Track` subject emits case labels
prefixed with `Std_Ui_Transition_Easing_` instead of
`Std_Ui_Grid_Track_`:

```go
func Std_Ui_Grid_trackToCss(t Std_Ui_Grid_Track) string {
    return func() string {
    switch __subject := t.(type) {
    case Std_Ui_Transition_Easing_Fr_V:   // <-- wrong prefix!
        n := __subject.V0
        ...
    case Std_Ui_Transition_Easing_TrackPx_V:
        ...
```

`Std_Ui_Grid_Track` ITSELF is emitted as an interface (not the
legacy enum `type ... = int`), which means
`shouldEmitSealedIface` is returning True for `Track` AS WELL as
for `Easing` — even though Track is not in the allowlist.

## Working hypotheses

Track passing `shouldEmitSealedIface`'s gate is the load-bearing
mystery. The gate, in `Compile.hs:912`:

```haskell
shouldEmitSealedIface modName typeName vars opts
    | opts == Can.Enum                                   = False  -- (1)
    | opts == Can.Unbox                                  = False  -- (1b)
    | not (null vars)                                    = False  -- (2)
    | qualifiedName `Set.member` rtBuilderShadowList     = False  -- (3)
    | qualifiedName `Set.member` sealedIfaceFlipAllowList = True  -- (4)
    | otherwise                                          = False  -- (5)
  where
    qualifiedName = ModuleName.toString modName ++ "." ++ typeName
```

For Track this evaluates to:
- `Std.Ui.Grid.Track` is `Can.Normal` (multi-variant, mixed
  payload — `Fr Int | Px Int | Auto | MinContent | ... | Repeat
  Int Int | ...`)
- vars empty (monomorphic)
- not in `rtBuilderShadowList`
- not in `sealedIfaceFlipAllowList`

→ Expected result: False (guard 5).
→ Observed result: True (Track emits as interface).

Hypotheses, ordered most→least likely:

### H1 — `_cg_unionDetails` lookup is returning Easing's metadata for Track's goKey

`subjectIsSealedIface` looks up by `qualifiedGoKey ctx home name`,
which uses the module-name + bare name as the key
(`Std_Ui_Grid_Track` for Track). The result tuple's `declHome` /
`name` is then passed to `shouldEmitSealedIface`.

If the lookup ever returned Easing's tuple under Track's key —
e.g. via a key collision in dep-module emission, OR via
contaminated state from a prior `withScopedDepModule` install —
the gate would receive `declHome="Std.Ui.Transition"`,
`name="Easing"`, `opts=Can.Normal`, vars=[]`, with
`qualifiedName="Std.Ui.Transition.Easing"` matching allowlist →
True.

Where to look: `populateUnionDetails` / `mergeUnionDetails` /
all sites that write into `_lc_unionDetails` or `_cg_unionDetails`.

### H2 — `derivedSealedIfaceNames` is over-populating the codegen env

The set lives in `Rec._cg_sealedIfaceNames` (per-codegen-env), but
it's computed once from `_cg_unionDetails`. If the union-details
map is populated AFTER `derivedSealedIfaceNames` runs (or
recomputed at a point where the map is partially set), the
derivation would return stale entries.

Where to look: `withSealedIfaceNames` setter (Compile.hs:~5991)
and every `Rec.withUnionDetails`/`_cg_unionDetails` write site.

### H3 — A different emitter path bypasses both the gate and `subjectIsSealedIface`

Perhaps the ADT-DECLARATION emitter (`emitSealedIfaceUnion` vs
legacy SkyADT alias) consults a different predicate than the
CASE-EMISSION path. If they disagree (one says "emit as iface"
while the other says "emit legacy case"), the mismatch surfaces
as cross-ADT prefix pollution.

Where to look: every call site of `emitSealedIfaceUnion` and
every call site of `caseToGoSealedIface` — both should consult
the same `shouldEmitSealedIface` decision or the same
`_cg_sealedIfaceNames` set.

### H4 — `Map.lookup` in `LC._lc_unionDetails` returns the WRONG entry due to a key-name collision

`qualifiedGoKey` for a dep-module ADT keys by
`modPrefix ++ "_" ++ name`. If two ADTs in two different modules
share a bare name (`Track` exists in `Std.Ui.Grid`), and one of
their qualifying module names happens to share a prefix with the
other's, the key could collide.

Where to look: every Sky ADT named `Track` (only one: Grid), and
every ADT whose module prefix could collide with another via the
"." → "_" substitution.

## Reproduction recipe

1. Add `"Std.Ui.Transition.Easing"` to `sealedIfaceFlipAllowList`
   in `src/Sky/Build/Compile.hs:982`.
2. Rebuild compiler: `./scripts/build.sh`.
3. Build the affected example:
   ```
   cd examples/26-ui-showcase
   rm -rf sky-out .skycache .skydeps
   /Users/anzel/works/playground/sky/sky-out/sky build src/Main.sky
   ```
4. Observe: `go build` fails with `undefined: Std_Ui_Transition_Easing_Fr_V`.
5. Inspect `examples/26-ui-showcase/sky-out/main.go` lines
   2825-2880 to confirm Track's `trackToCss` emits with wrong
   prefix.

## Suggested diagnosis path

1. Add `Debug.Trace.trace` to `subjectIsSealedIface` just before
   the `shouldEmitSealedIface` call, printing
   `(home, name, goKey, vars, opts, gate-result)` for EVERY
   subject seen during Track's `trackToCss` lowering.
2. Cross-reference with the keys present in `_lc_unionDetails` at
   that moment — does the map contain a stale or wrong entry
   under `Std_Ui_Grid_Track`?
3. Add a parallel trace to `derivedSealedIfaceNames` showing what
   names go into `_cg_sealedIfaceNames`.
4. If the set wrongly includes Track, follow the population
   chain back to find where Track gets admitted.

## Implication for sealed-iface rollout

Until this is closed, sealed-iface flips MUST satisfy this rule:

**Every new flip MUST be in a module that contains NO OTHER
`Can.Normal` multi-variant ADT, OR the flip's ADT is the ONLY
sealed-iface ADT in the compilation unit.**

This rules out:

- `Std.Ui.Transition.{Easing, Step}` — both `Can.Normal` (sibling ADTs)
- `Std.Ui.{HAlign, VAlign, Location, PseudoClass}` — all `Can.Enum` (blocked by guard 1 anyway)
- `Sky.Http.Server.{Route, Server, Cookie}` — Server.sky declares three
- `Std.Db.{SqlValue, SqlField}` — in carve-out, but cross-ADT module

Iter 65's Iterations flip succeeded ONLY because
`Std.Ui.Animation` happens to have just one `Can.Normal` ADT
(Iterations; FillMode is `Can.Enum` so it doesn't trip the bug).

## Owner

Tracked under task #677 (v0.17 architectural close — sealed-
interface ADT emission). Resolves on or before the next flip
attempt.
