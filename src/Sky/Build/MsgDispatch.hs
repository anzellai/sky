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
    , emitRegisterMsgVariantLine
    , isMsgShapedUnion
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
