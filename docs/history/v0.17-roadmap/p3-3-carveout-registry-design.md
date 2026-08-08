# P3.3 design: per-ADT carve-out registry + decision function — REVISED

**STATUS**: iter 49 v2 (post dual-grill). Both grillers caught
fatal flaws in the original draft:
- Half the module names were fictional (`Sky.Db.Sql.*` doesn't exist;
  actual `Std.Db.*`; `Sky.Decimal.Internal` doesn't exist;
  `Sky.Http.WebSocketServer` doesn't exist).
- ErrorInfo was on the list but it's a record alias, not an ADT.
- The env-flag `unsafePerformIO $ lookupEnv` pattern would CAF-cache
  the result process-wide (per established `dceDisabled` pattern in
  the same file), breaking the spec's env-toggle test.
- Std.Html.Html / Std.Html.Attribute look like they're missed but
  they're PARAMETRIC (`type Html msg` / `type Attribute msg`) so
  rule 2 catches them — no action needed.

Empirically-verified carve-out list (grepping sky-stdlib for type
definitions + rt-side SkyADT constructions):

| Qualified name | rt-side constructor |
|---|---|
| `Sky.Core.Error.Error` | `rt.go:3886` `skyErrorAdt` |
| `Std.Db.SqlValue` | `db_decoder.go:236/259/261` Money/CurrencyRaw |
| `Std.Db.SqlField` | `db_auth.go` SetField switch |
| `Std.Decimal.Decimal` | `decimal_kernel.go:34` Decimal__Internal |
| `Sky.Http.Server.Stream.StreamWriter` | `server_stream.go:385` |
| `Sky.Core.WebSocket.WebSocketMessage` | `websocket.go:663-687` |
| `Sky.Http.Server.WebSocket.WebSocketServer` | `server_websocket.go:377` |
| `Sky.Core.Http.Stream.ChunkEvent` | `http_stream.go:747/753/759` |

Sky.Core.Error.ErrorKind not on list — rt doesn't construct it as
SkyADT (it's a Can.Enum so rule 1 carves it anyway).

Sky.Core.Error.ErrorInfo not on list — `type alias`, not ADT.

Std.Html.Html / Std.Html.Attribute not on list — parametric (`msg`
type var), rule 2 carves them.

## Design (revised)

Pure function — NO env lookup, no IORef, no NOINLINE. Returns
False by default (legacy path) until P3.4 wires the real call
sites. P3.3 ships infrastructure only.

```haskell
-- | v0.17 sealed-iface ADT carve-out decision (P3.3 infrastructure).
-- Returns True if codegen should emit the new sealed-iface + variant
-- struct shape for this ADT; False to keep the legacy
-- `type X = rt.SkyADT` alias path.
--
-- Rules (in priority order):
--   1. Can.Enum (all-nullary) → False — stays as `type X int + iota`.
--   2. Polymorphic (vars non-empty) → False — covered by P4.
--   3. rtBuilderShadowList → False — rt-side Go code constructs
--      SkyADT-shape values for these; the user's case-of would
--      type-switch on a non-existent variant struct and default-
--      panic.
--   4. Otherwise → False until P3.4. P3.4 wires real selection
--      (env flag / CLI / per-call-site decision).
--
-- Pure — no IORef reads, no env lookups. Spec-testable in isolation.
-- P3.4 will add the True-returning branch at the call sites where
-- it has the build-level context to decide.
shouldEmitSealedIface
    :: ModuleName.Canonical
    -> String
    -> [T.TVar]
    -> Can.UnionOpts
    -> Bool
shouldEmitSealedIface modName typeName vars opts
    | opts == Can.Enum                           = False
    | not (null vars)                            = False
    | qualifiedName `Set.member` rtBuilderShadowList = False
    | otherwise                                  = False  -- P3.3: default
  where
    qualifiedName = ModuleName.toString modName ++ "." ++ typeName

-- | Hardcoded carve-out — rt-side Go builders construct SkyADT-shape
-- values for these ADTs. Migrating them to sealed-iface would
-- desync builder + consumer.
--
-- AUDIT GATE: change to this list must update
-- `Sky.Build.SealedIfaceCarveoutSpec`'s explicit enumeration test.
rtBuilderShadowList :: Set.Set String
rtBuilderShadowList = Set.fromList
    [ "Sky.Core.Error.Error"
    , "Std.Db.SqlValue"
    , "Std.Db.SqlField"
    , "Std.Decimal.Decimal"
    , "Sky.Core.Http.Stream.ChunkEvent"
    , "Sky.Http.Server.Stream.StreamWriter"
    , "Sky.Core.WebSocket.WebSocketMessage"
    , "Sky.Http.Server.WebSocket.WebSocketServer"
    ]
```

## Spec (audit gate)

```haskell
describe "Sky.Build.shouldEmitSealedIface" $ do
    let mkMod s = ModuleName.Canonical (Module.toText s)  -- adapt to actual API
    let modColor = mkMod "Mod.Color"
    let modError = mkMod "Sky.Core.Error"

    it "P3.3: defaults to False (legacy)" $
        shouldEmitSealedIface modColor "Color" [] Can.Normal `shouldBe` False
    it "Can.Enum rejected" $
        shouldEmitSealedIface modColor "Color" [] Can.Enum `shouldBe` False
    it "Polymorphic rejected" $
        shouldEmitSealedIface modColor "Box" ["a"] Can.Normal `shouldBe` False
    it "Sky.Core.Error.Error in shadow list" $
        shouldEmitSealedIface modError "Error" [] Can.Normal `shouldBe` False

    -- Explicit-enumeration audit gate: this fails the build if the
    -- shadow list is silently grown/shrunk without spec update.
    it "rtBuilderShadowList is exactly this set" $
        Set.toAscList rtBuilderShadowList `shouldBe` Set.toAscList (Set.fromList
            [ "Sky.Core.Error.Error"
            , "Std.Db.SqlValue"
            , "Std.Db.SqlField"
            , "Std.Decimal.Decimal"
            , "Sky.Core.Http.Stream.ChunkEvent"
            , "Sky.Http.Server.Stream.StreamWriter"
            , "Sky.Core.WebSocket.WebSocketMessage"
            , "Sky.Http.Server.WebSocket.WebSocketServer"
            ])
```

## Co-dependency note (Griller 2 B1)

`SqlField` carries `SqlValue` in `SetField SqlValue`. Both must
stay carved-out together. If a future iter drops `SqlValue` from
the list but keeps `SqlField`, the user's `case sf of SetField v
-> ...` extracts a legacy-shape v from a sealed-iface SqlValue
context — runtime panic. Spec asserts both on the list.

## Implementation surface

~30 LOC in `src/Sky/Build/Compile.hs` (function + Set + EXPORT
both from module). ~70 LOC in
`test/Sky/Build/SealedIfaceCarveoutSpec.hs`.

## Original draft (PRESERVED for grill-checklist reference)


## Problem (from iter 46 grill B1 + B2)

The codegen flip (P3.4) needs a per-ADT decision: "lower this Sky ADT
as sealed interface + variant structs (new), or keep as `type X =
rt.SkyADT` alias (legacy)?". A global env flag is WRONG because:

* rt-side builders construct SkyADT-shaped values for ~5 stdlib ADTs
  (Sky.Core.Error / Sky.Db.Sql.SqlValue / Sky.Db.Sql.SqlField /
  Sky.Decimal.Internal / Sky.Http.StreamMsg / WebSocket).
* If those ADTs migrate to sealed-iface, the user's `case kind of
  Io -> ...` type-switches on `Sky_Core_Error_Io_V` — which never
  exists because rt's `ErrIo()` still returns `SkyADT{Tag:0, SkyName:
  "Io"}`. Default panic on first error.
* Decision MUST be per-ADT, with the carve-out list explicit + audited.

## Design

Pure addition to `Sky.Build.Compile` — a decision function consulted
by both emission (P3.4) and pattern-match codegen (P3.5). No callers
yet; safe to land standalone.

```haskell
-- | v0.17 sealed-iface ADT carve-out. Returns True if codegen should
-- emit the new sealed-iface + variant struct shape for this ADT;
-- False to keep the legacy `type X = rt.SkyADT` alias path.
--
-- Carve-out rules (in priority order):
--   1. `Can.Enum` (all-nullary) ADTs stay on the `type X int + iota`
--      shape — no SkyVariant interface needed at all.
--   2. Polymorphic ADTs (vars non-empty) stay legacy in P3.4. P4
--      covers the parametric story (Element / Attribute / Html
--      phantom-msg + Maybe / Result / Task real-flow).
--   3. ADTs in the rt-builder-shadow-list stay legacy because rt-side
--      Go code constructs SkyADT-shape values for them:
--        * Sky.Core.Error             — rt.ErrIo / makeError / etc.
--        * Sky.Core.Error.ErrorInfo   — SkyErrorInfo embedded shape
--        * Sky.Db.Sql.SqlValue        — db_auth.go switch on .SkyName
--        * Sky.Db.Sql.SqlField        — db_auth.go switch on .SkyName
--        * Sky.Decimal.Internal       — decimal_kernel.go .SkyName
--        * Sky.Http.StreamMsg         — http_stream.go .SkyName
--        * Sky.Http.WebSocketServer   — websocket.go reflect.Field
--   4. Default: SKY_VARIANT_ADT env flag (off by default in P3.3;
--      P3.4 wires the flag to actual emission). Returns True when
--      the env flag is set AND none of the above carve-out rules
--      apply.
--
-- Implementation: pure function, no IORef reads. Carve-out list
-- hardcoded as a `Set String` of module-qualified type names.
-- Audit hook: a Hspec spec asserts the list matches an EXPLICIT
-- enumeration so silently adding/removing entries fails the build.
shouldEmitSealedIface
    :: ModuleName.Canonical  -- ^ module the ADT is declared in
    -> String                 -- ^ unqualified type name
    -> [T.TVar]               -- ^ ADT's type variables (empty = monomorphic)
    -> Can.UnionOpts          -- ^ Can.Enum / Can.Normal
    -> Bool
shouldEmitSealedIface modName typeName vars opts
    | opts == Can.Enum            = False  -- (1)
    | not (null vars)             = False  -- (2)
    | qualifiedName `Set.member` rtBuilderShadowList = False  -- (3)
    | otherwise                   = envFlagOn  -- (4)
  where
    qualifiedName = ModuleName.toString modName ++ "." ++ typeName

    rtBuilderShadowList :: Set.Set String
    rtBuilderShadowList = Set.fromList
        [ "Sky.Core.Error.Error"
        , "Sky.Core.Error.ErrorKind"
        , "Sky.Core.Error.ErrorInfo"
        , "Sky.Db.Sql.SqlValue"
        , "Sky.Db.Sql.SqlField"
        , "Sky.Decimal.Internal"          -- if exists; verify in iter
        , "Sky.Http.Server.Stream.StreamMsg"
        , "Sky.Http.WebSocketServer"      -- typed-FFI shape
        ]

    envFlagOn = unsafePerformIO $ do
        v <- lookupEnv "SKY_VARIANT_ADT"
        return (v == Just "1")
{-# NOINLINE shouldEmitSealedIface #-}
```

NOINLINE pragma prevents CAF memoisation of the env lookup (per
iter 41-44 CAF lesson). Each call re-reads the env. Cost: trivial,
called at most ~50 times per build (once per ADT).

## Verification

Two specs that exercise the decision function in isolation:

```haskell
-- test/Sky/Build/SealedIfaceCarveoutSpec.hs
describe "Sky.Build.shouldEmitSealedIface" $ do
    it "rejects Can.Enum regardless of env or vars" $
        shouldEmitSealedIface modA "Color" [] Can.Enum `shouldBe` False
    it "rejects polymorphic ADTs regardless of env" $
        shouldEmitSealedIface modA "Maybe" ["a"] Can.Normal `shouldBe` False
    it "rejects Sky.Core.Error.Error even with flag on" $ do
        setEnv "SKY_VARIANT_ADT" "1"
        shouldEmitSealedIface coreError "Error" [] Can.Normal `shouldBe` False
        unsetEnv "SKY_VARIANT_ADT"
    it "rejects every entry in rtBuilderShadowList" $
        forM_ rtBuilderShadowListEnum $ \qn ->
            shouldEmitSealedIface (parseMod qn) (parseType qn) [] Can.Normal
                `shouldBe` False
    it "accepts non-carve-out monomorphic when env flag set" $ do
        setEnv "SKY_VARIANT_ADT" "1"
        shouldEmitSealedIface modA "Color" [] Can.Normal `shouldBe` True
        unsetEnv "SKY_VARIANT_ADT"
    it "defaults to False when env unset (legacy emission)" $ do
        unsetEnv "SKY_VARIANT_ADT"
        shouldEmitSealedIface modA "Color" [] Can.Normal `shouldBe` False
    it "carve-out list explicit enumeration" $
        rtBuilderShadowListEnum `shouldBe`
            [ "Sky.Core.Error.Error"
            , "Sky.Core.Error.ErrorKind"
            , "Sky.Core.Error.ErrorInfo"
            , "Sky.Db.Sql.SqlValue"
            , "Sky.Db.Sql.SqlField"
            , "Sky.Decimal.Internal"
            , "Sky.Http.Server.Stream.StreamMsg"
            , "Sky.Http.WebSocketServer"
            ]
```

Last spec is the audit gate — silently changing the carve-out list
fails the spec.

## Risks for grillers

1. **Carve-out list completeness**: rt-builder grep audit (iter 46
   audit + iter 47 audit) found 5+ modules. Did we miss any? Re-grep
   rt/*.go for `switch adt.SkyName` and `adt.(SkyADT)` patterns.
2. **`Sky.Decimal.Internal`**: does this type actually exist in
   sky-stdlib? Verify path. If it's actually `Std.Decimal.Internal`,
   the qualified name is wrong.
3. **Module-name format**: `ModuleName.toString` returns dotted
   form (`Sky.Core.Error`) — does the codegen actually call us with
   the canonical name format, or does it go through the underscore-
   prefixed Go-mangled form (`Sky_Core_Error`)? Format mismatch =
   carve-out never fires.
4. **Polymorphic carve-out (rule 2)**: P3.4 covers monomorphic only.
   But what about `Sky.Db.Sql.SqlValue` if it has type vars in
   future? Today verify: is SqlValue parametric?
5. **Env-flag semantics**: when SKY_VARIANT_ADT is unset, function
   returns False for EVERY ADT. That's the safe default — no
   behaviour change until env is explicitly set. But P3.4's pattern-
   match codegen ALSO consults this function. If two callsites
   read the env at different times in the same build, results
   could diverge. NOINLINE forces fresh read each time — but env
   values can change mid-process? In practice no, but document
   the assumption.
6. **Empty rtBuilderShadowList accidentally**: typo in the Set
   literal would silently un-carve-out. Mitigation: the explicit-
   enumeration spec.

## Implementation surface

~80 LOC in `src/Sky/Build/Compile.hs` (new function + the hardcoded
Set + NOINLINE pragma). ~120 LOC in
`test/Sky/Build/SealedIfaceCarveoutSpec.hs` (7 cases above).

No callers yet — P3.4 wires it. Safe to commit standalone.
