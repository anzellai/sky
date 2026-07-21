-- | v0.17 PR-22 S0 — registries of runtime type mappings, extracted
-- from 'Sky.Build.Compile' so 'Sky.Generate.Go.Type.mapNamedType'
-- can consult them without importing the @Compile@ module (which
-- would re-introduce the Compile↔Type cycle that 'buildMappingContext'
-- currently dances around via 'Rec.CodegenEnv').
--
-- Every registry is PURE (no IORef, no env-derived data) — they live
-- here as static constants.  The MappingContext fields that lift them
-- (PR-22 S0/S1) carry these values forward into the pipeline.
--
-- DO NOT add env-derived data here.  Env-derived registries
-- ('mcRecordAliases', 'mcUnionNames', 'mcEnumNames', 'mcAliases')
-- belong in 'MappingContext' and get populated via
-- 'buildMappingContext'.
module Sky.Generate.Go.RuntimeMaps where


-- | Types from Sky runtime that don't have Go type definitions.
-- These map to @"any"@ in Go because they're internal abstractions.
--
-- Consumed by 'mapNamedType' AFTER 'runtimeTypedMap' / record-alias
-- match — the legacy lookup order at 'Compile.hs:17407-17416' is
-- @matches → runtimeTyped → unionRecovery → runtimeOnly/union/any@.
runtimeOnlyTypes :: [String]
runtimeOnlyTypes =
    [ "Decoder", "Value", "Attribute", "Handler"
    , "Route", "Middleware", "Session", "Store"
    ]


-- | Parameterised opaque types that collapse to a Go alias
-- irrespective of their type arguments.
--
-- @Decoder String@, @Decoder Int@, etc. all emit as @rt.SkyDecoder@
-- because under the hood the runtime uses a single
-- @type SkyDecoder = any@.
--
-- This is consumed by the user-ADT fallback when args are non-empty
-- but the type is in the opaque-parameterised set.
opaqueParameterisedGoTy :: String -> Maybe String
opaqueParameterisedGoTy "Decoder" = Just "rt.SkyDecoder"
opaqueParameterisedGoTy "Value"   = Just "rt.SkyValue"
opaqueParameterisedGoTy _         = Nothing


-- | Module-qualified overrides that win over the bare-name mapping.
--
-- Needed when the same short type name lives in two stdlib modules
-- with distinct Go representations — e.g. @Sky.Core.Http.Response@
-- (HTTP client response struct) vs @Sky.Http.Server.Response@
-- (server response struct).  Without this, the bare-name lookup
-- wrongly collapses them onto the same Go type and user code
-- panics with @interface conversion: interface {} is rt.HttpResponse,
-- not rt.SkyResponse@ (or vice versa).
--
-- We list both the full module path (e.g. @"Sky.Core.Http"@) and the
-- common import alias (e.g. @"Http"@) because the canonicaliser's
-- @resolveTypeQual@ preserves the user-written qualifier for non-
-- builtin modules — so @Http.Response@ lands in the solved type
-- with home = @"Http"@, not @"Sky.Core.Http"@.
--
-- LOOKUP ORDER (pipeline): qualifiedRuntimeTypedMap fires BEFORE
-- runtimeTypedMap so the disambiguation wins.
qualifiedRuntimeTypedMap :: [((String, String), String)]
qualifiedRuntimeTypedMap =
    [ (("Sky.Core.Http",   "Response"), "rt.HttpResponse")
    , (("Http",            "Response"), "rt.HttpResponse")
    , (("Sky.Http.Server", "Response"), "rt.SkyResponse")
    , (("Server",          "Response"), "rt.SkyResponse")
    ]


-- | Known runtime types that have concrete Go type definitions.
-- These map to their Go type name (with @rt.@ prefix).
--
-- Each alias is declared as @type SkyX = any@ in @runtime-go/rt@ so
-- there's no boxing/unboxing overhead and legacy any-typed values
-- assign/compare transparently.  Note: @Route@ is deliberately NOT
-- here — there's already an exported @SkyRoute@ STRUCT used by the
-- router, and the Sky-side @Route@ value is an unexported
-- @liveRoute@ struct, so mapping to @SkyRoute@ would be a lie.
-- (Route lives in 'runtimeOnlyTypes' instead.)
runtimeTypedMap :: [(String, String)]
runtimeTypedMap =
    [ ("VNode",      "rt.VNode")
    , ("Request",    "rt.SkyRequest")
    , ("Response",   "rt.SkyResponse")
    , ("Cmd",        "rt.SkyCmd")
    , ("Sub",        "rt.SkySub")
    , ("Decoder",    "rt.SkyDecoder")
    , ("Value",      "rt.SkyValue")
    , ("Attribute",  "rt.SkyAttribute")
    , ("Handler",    "rt.SkyHandler")
    , ("Middleware", "rt.SkyMiddleware")
    , ("Session",    "rt.SkySession")
    , ("Store",      "rt.SkyStore")
    -- Sky.Core.Error.Error is the canonical Sky error type.
    -- Call-site coercion (ResultCoerce / TaskCoerceT) may see
    -- the type with home stripped — emit the qualified Go alias.
    -- The alias declaration @type Sky_Core_Error_Error = rt.SkyADT@
    -- is auto-emitted as part of the Sky.Core.Error dep compilation
    -- (always reachable via the Std.Error transitive import in the
    -- v0.10.0 consolidation).
    , ("Error",      "Sky_Core_Error_Error")
    -- v0.13 A2 follow-up: kernel @Http.get@/@Http.post@ declare
    -- their return type with empty @home@ and name @HttpResponse@.
    -- Once A2's pre-registration connects forward refs (e.g. an
    -- unannotated @checkResponseStatus resp@ param), the renderer
    -- sees @T.TType "" "HttpResponse" []@ and otherwise emits
    -- bare @HttpResponse@ (undefined Go).  Maps to the existing
    -- runtime struct.
    , ("HttpResponse", "rt.HttpResponse")
    -- Db is stored as a pointer at runtime — Db_connect/Db_open
    -- return @&SkyDb{…}@.  Typing as @*rt.SkyDb@ matches the
    -- @Ok[any,any](db)@ branch so the ResultCoerce type assertion
    -- on the OkValue succeeds.
    , ("Db",         "*rt.SkyDb")
    , ("Stmt",       "rt.SkyStmt")
    , ("Row",        "rt.SkyRow")
    , ("Conn",       "rt.SkyConn")
    ]
