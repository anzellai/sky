-- | Sky.Build.MsgDispatch — pure helper that enumerates ADT
-- variants for the upcoming per-Msg typed dispatch table emission
-- (v0.17 Phase 4, Stage 1).
--
-- This module is the foundation of the "perMsgTypedDispatch" lever
-- (`docs/v0.17-roadmap/phase4-per-msg-dispatch.md`).  At codegen
-- time the compiler already knows the full Msg ADT shape — tag,
-- constructor name, constructor arity, and typed constructor
-- parameters.  Stage 1 introduces a pure helper that exposes the
-- variant metadata in a single immutable shape so later stages
-- (typed update arm emission, dispatch table emission, wire decoder
-- emission) can consume it from one source of truth rather than
-- re-walking @Can.Union@ values per emission site.
--
-- Why a separate module?  Three reasons:
--
--   * Pure helper, no IORef.  Per CLAUDE.md §0 hard rule 3, this
--     pass MUST NOT introduce load-bearing module-level state.
--     'collectMsgVariants' is a deterministic function of a
--     'Can.Module' value, no IO, no mutation.
--
--   * Test surface.  The helper has its own unit spec
--     ('Sky.Build.MsgDispatchSpec') that asserts the variant
--     enumeration shape without spinning up the codegen pipeline.
--     Future Phase 4 stages reuse this surface to assert
--     typed-arm + dispatch-table + wire-decoder emission shapes.
--
--   * Stable interface.  Phase 4 Stage 1 emits a Stage-1
--     observable (the @rt.RegisterMsgUpdate@ scaffolding line per
--     ADT) before the typed-arm machinery lands; downstream stages
--     consume the helper's pure output without re-discovering it.
--
-- File layout (from the phase 4 design):
-- @
-- src/Sky/Build/MsgDispatch.hs       ←  this file (NEW)
-- runtime-go/rt/msg_dispatch.go      ←  registries (NEW)
-- test/Sky/Build/MsgDispatchSpec.hs  ←  unit specs (NEW)
-- runtime-go/rt/msg_dispatch_test.go ←  runtime tests (NEW)
-- @
module Sky.Build.MsgDispatch
    ( MsgVariant (..)
    , MsgUnion (..)
    , collectMsgVariants
    , variantsFromUnion
    , emitRegisterUpdateLine
    , emitRegisterUpdateLineWithTable
    , emitRegisterMsgVariantLine
    , emitDispatchTableVarDecl
    , emitDispatchTableInitLines
    , dispatchTableVarName
    , isMsgShapedUnion
    , decoderFuncName
    , emitRegisterMsgDecoderLine
    , variantHasTypedPayload
    ) where

import qualified Data.Map.Strict   as Map

import qualified Sky.AST.Canonical as Can


-- | Per-variant metadata extracted from a @Can.Union@'s
-- constructor list.  Stable shape that downstream Phase 4 emission
-- helpers can consume without re-walking @Can.Ctor@ values.
data MsgVariant = MsgVariant
    { _mv_name    :: !String     -- ^ Constructor name (e.g. @Increment@).
    , _mv_tag     :: !Int        -- ^ Tag index within the union.
    , _mv_arity   :: !Int        -- ^ Constructor arity (number of typed payload params).
    , _mv_argTys  :: ![Can.Type] -- ^ Typed payload parameter types.  Empty when arity = 0.
    } deriving (Show, Eq)


-- | Per-union metadata.  Carries the bare type name + the variant
-- list in declaration order.  Constructor options ('Can.CtorOpts')
-- are preserved so callers can filter out @Enum@ unions (which
-- already short-circuit through the integer-tag path and don't
-- need typed dispatch) or @Unbox@ unions (single-ctor wrappers
-- with no dispatch surface).
data MsgUnion = MsgUnion
    { _mu_typeName :: !String          -- ^ Bare type name (e.g. @Msg@).
    , _mu_opts     :: !Can.CtorOpts    -- ^ @Normal@ / @Enum@ / @Unbox@.
    , _mu_vars     :: ![String]        -- ^ Source-ADT type-variable names.
    , _mu_variants :: ![MsgVariant]    -- ^ Variants in declaration order.
    } deriving (Show)


-- | Extract every union from a 'Can.Module', returning a
-- 'MsgUnion' per declared ADT in alphabetical order on the type
-- name.  Deterministic: same input module always produces the
-- same output list.
--
-- Stage 1 contract: emits ALL unions, not just Msg-shaped ones.
-- The @Live.app cfg.update@ entry-point detection lives in a
-- later stage; Stage 1's responsibility is the enumeration
-- primitive.
collectMsgVariants :: Can.Module -> [MsgUnion]
collectMsgVariants canMod =
    [ MsgUnion
        { _mu_typeName = name
        , _mu_opts     = Can._u_opts union_
        , _mu_vars     = Can._u_vars union_
        , _mu_variants = variantsFromUnion union_
        }
    | (name, union_) <- Map.toAscList (Can._unions canMod)
    ]


-- | Convert a single 'Can.Union' to the variant list.  Exposed
-- separately so dep-module emission paths (which carry a
-- @ModuleName.Canonical@-prefixed type name) can reuse the
-- per-variant projection without re-keying.
variantsFromUnion :: Can.Union -> [MsgVariant]
variantsFromUnion (Can.Union _vars ctors _numAlts _opts) =
    [ MsgVariant
        { _mv_name   = cname
        , _mv_tag    = idx
        , _mv_arity  = arity
        , _mv_argTys = argTys
        }
    | Can.Ctor cname idx arity argTys <- ctors
    ]


-- | Filter predicate — does this union shape benefit from typed
-- dispatch emission?  Phase 4 skips:
--
--   * @Enum@ unions (all nullary; dispatch is a no-op since the
--     payload is empty).
--   * @Unbox@ unions (single-ctor wrappers; no dispatch decision
--     to make).
--   * Zero-variant unions (degenerate, defensive — Sky doesn't
--     emit these but guard anyway).
--
-- Stage 1 uses this only to size the emission decision; later
-- stages also gate on the union being the type-arg of
-- @Live.app cfg.update@ / @Tui.app cfg.update@ / @Webview.app
-- cfg.update@.
isMsgShapedUnion :: MsgUnion -> Bool
isMsgShapedUnion mu =
    case _mu_opts mu of
        Can.Enum  -> False
        Can.Unbox -> False
        Can.Normal ->
            not (null (_mu_variants mu))


-- | Emit the Stage 1 @rt.RegisterMsgUpdate@ scaffolding line per
-- ADT.  Stage 1 is foundation-only: the line registers the ADT
-- type name with @nil@ as the dispatch table value (the typed
-- map gets filled in by Stage 2 once the per-variant typed update
-- arms are emitted).
--
-- Lives next to the existing @rt.RegisterAdtTag@ calls in the Go
-- @init()@ block so the runtime sees the ADT-name → registry slot
-- before any user code can dispatch.
--
-- Stage 1 emission shape (per ADT, single line):
--
-- > rt.RegisterMsgUpdate("Main_Msg", nil)
--
-- The @nil@ payload is a placeholder.  Stage 2 replaces it with
-- the typed @map[int]func(payload any, model M) (M, rt.SkyCmd)@
-- literal once the typed arm functions exist.  The runtime
-- lookup (Stage 6) treats @nil@ as "no fast path; reflect
-- fallback" — byte-identical user-visible behaviour to today,
-- which is the Stage 1 correctness invariant.
emitRegisterUpdateLine :: String -> String
emitRegisterUpdateLine qualType =
    "rt.RegisterMsgUpdate(" ++ show qualType ++ ", nil)"


-- | v0.17 Phase 4 Stage 3 — emit @rt.RegisterMsgUpdate@ pointing at
-- the per-ADT dispatch table variable (instead of @nil@ in Stage 1).
--
-- The table variable is constructed via 'emitDispatchTableVarDecl' +
-- 'emitDispatchTableInitLines' at the same emission site (one
-- adjacent @var@ + one @init()@ population block per ADT).  This
-- replaces the Stage 1 placeholder with an actual map literal that
-- the runtime fast-path (Stage 6) can consult.
--
-- Stage 3 emission shape:
--
-- > rt.RegisterMsgUpdate("Main_Msg", Main_Msg_dispatch)
--
-- Where @Main_Msg_dispatch@ is the @map[int]any@ var emitted by
-- 'emitDispatchTableVarDecl' for the same ADT.
emitRegisterUpdateLineWithTable :: String -> String
emitRegisterUpdateLineWithTable qualType =
    "rt.RegisterMsgUpdate(" ++ show qualType ++ ", " ++ dispatchTableVarName qualType ++ ")"


-- | Per-ADT dispatch-table variable name.  Mangles the qualified
-- type name with a @_dispatch@ suffix.  Examples:
--
-- > "Main_Msg"        →  "Main_Msg_dispatch"
-- > "Lib_Auth_Action" →  "Lib_Auth_Action_dispatch"
--
-- Stable across Stage 3 + Stage 6 so the codegen + runtime fast
-- path stay in lockstep.
dispatchTableVarName :: String -> String
dispatchTableVarName qualType = qualType ++ "_dispatch"


-- | v0.17 Phase 4 Stage 3 — declare the dispatch-table variable.
--
-- Shape (per ADT):
--
-- > var Main_Msg_dispatch = map[int]any{}
--
-- Stage 3 contract: variable is declared at package scope (next to
-- the existing ctor/arm declarations) and POPULATED via per-init()
-- assignments emitted by 'emitDispatchTableInitLines'.  We don't
-- use a single-shot map literal because emission iterates per-ctor
-- and map-literal entries don't compose across multiple emission
-- arms cleanly without intermediate buffering.
--
-- Value type is @any@ (not the typed @func(any, M) (M, rt.SkyCmd)@
-- shape sketched in the design doc).  Reasoning:
--
--   * The model type @M@ is not known at ADT-declaration time — it
--     comes from the @Live.app cfg.update@ type-arg discovered only
--     at the @Live.app@ call site (a later stage's job).
--   * Stage 3's deliverable is the OBSERVABLE registration shape:
--     a non-nil dispatch table the runtime can lookup.  Stage 6
--     wires the typed fast-path that consumes it.
--   * Using @any@ keeps Stage 3 byte-identical to today on the
--     user-visible runtime behaviour — Stage 6 is what flips the
--     fast-path consumer on.
--
-- Returns @""@ for ADT shapes that don't get a dispatch table
-- (Enum / Unbox / zero-variant).
emitDispatchTableVarDecl :: String -> [MsgVariant] -> String
emitDispatchTableVarDecl qualType vs
    | null vs = ""
    | otherwise =
        "var " ++ dispatchTableVarName qualType ++ " = map[int]any{}"


-- | v0.17 Phase 4 Stage 3 — populate the dispatch table inside the
-- per-union @func init()@ block.
--
-- One line per variant of the form:
--
-- > Main_Msg_dispatch[0] = Main_Msg_arm_Increment;
--
-- The arm function symbol is the per-variant typed delegation
-- emitted by 'emitMsgArmFuncs' (Stage 2).  Population order matches
-- declaration order, but lookup is keyed by tag — order is purely
-- cosmetic for the emitted code.
--
-- Returns @""@ for ADT shapes 'emitDispatchTableVarDecl' also
-- skips, so the gate stays paired.
emitDispatchTableInitLines :: String -> [MsgVariant] -> String
emitDispatchTableInitLines qualType vs
    | null vs = ""
    | otherwise =
        concatMap (\mv ->
            dispatchTableVarName qualType
                ++ "[" ++ show (_mv_tag mv) ++ "] = "
                ++ qualType ++ "_arm_" ++ _mv_name mv ++ "; ")
            vs


-- | Emit one @rt.RegisterMsgVariant@ scaffolding line per
-- variant.  Stage 1 records the (union, ctor) → tag mapping
-- alongside the existing @rt.RegisterAdtTag@ surface; later
-- stages add a typed-payload accessor to the value slot.
--
-- Stage 1 emission shape (per variant, single line):
--
-- > rt.RegisterMsgVariant("Main_Msg", "Increment", 0, 0)
--
-- Args: qualified ADT type name, constructor name, tag index,
-- arity.  The arity carries through to Stage 5 (wire decoder)
-- so the @applyMsgArgs@ fast path can short-circuit on
-- zero-arity ctors without spinning up @json.Unmarshal@.
emitRegisterMsgVariantLine :: String -> MsgVariant -> String
emitRegisterMsgVariantLine qualType mv =
    "rt.RegisterMsgVariant("
        ++ show qualType ++ ", "
        ++ show (_mv_name mv) ++ ", "
        ++ show (_mv_tag mv) ++ ", "
        ++ show (_mv_arity mv) ++ ")"


-- | v0.17 Phase 4 Stage 4 — per-variant wire decoder function name.
-- Mangled @<qualType>_decode_<ctor>@ so it lives in the same
-- namespace as the per-variant arm functions (Stage 2).
--
-- > "Main_Msg"  +  "DoSignIn"  →  "Main_Msg_decode_DoSignIn"
decoderFuncName :: String -> String -> String
decoderFuncName qualType ctorName =
    qualType ++ "_decode_" ++ ctorName


-- | v0.17 Phase 4 Stage 4 — predicate: is this variant a candidate
-- for typed wire decoder emission?
--
-- Stage 4 contract: emit a typed decoder ONLY when the variant has
-- arity > 0.  Zero-arity variants don't carry a payload — the
-- runtime fast-path can directly return the @arm_<ctor>()@ value
-- without consulting a decoder.  Variants with arity > 0 always
-- emit a decoder; the per-slot type may still resolve to @any@
-- (rt.AnyValue handling) but the decoder shape is correct.
variantHasTypedPayload :: MsgVariant -> Bool
variantHasTypedPayload mv = _mv_arity mv > 0


-- | v0.17 Phase 4 Stage 4 — emit the @rt.RegisterMsgDecoder@ line
-- inside the per-union @func init()@ block, one per qualified
-- variant.  Keyed by bare ctor name to match
-- 'LookupMsgDecoder''s lookup contract (the wire path uses the
-- in-band ctor name without first resolving the ADT).
--
-- Shape (per variant):
--
-- > rt.RegisterMsgDecoder("DoSignIn", Main_Msg_decode_DoSignIn)
--
-- Stage 4 is foundation-only on the runtime consumer side: Stage 6
-- wires the consult into 'applyMsgArgs'.  The registration is
-- byte-identical-behaviour on user-visible runtime (lookup is
-- exposed but no production path consults it yet).
emitRegisterMsgDecoderLine :: String -> MsgVariant -> String
emitRegisterMsgDecoderLine qualType mv =
    "rt.RegisterMsgDecoder("
        ++ show (_mv_name mv) ++ ", "
        ++ decoderFuncName qualType (_mv_name mv) ++ ")"
