{-# LANGUAGE LambdaCase #-}
-- | Phase C plumbing: convert the producer-side FtyAst (parsed
-- from kernel.json's skyType field) into a canonical 'Can.Type' /
-- 'Can.Annotation' that the constraint generator can plug into a
-- @T.CForeign@ for the matching @Can.VarKernel@ reference.
--
-- Lives in Sky.Build (alongside FfiRegistry / FfiTypeParser) so the
-- Sky.Type.Constrain layer doesn't import producer-side modules.
-- The Sky.Type side just queries the Map populated here.
module Sky.Build.FfiTypeResolve
    ( ftyToAnnotation
    , ftyToType
    ) where

import Data.List (nub)
import qualified Data.Map.Strict as Map
import qualified Sky.AST.Canonical as Can
import qualified Sky.Sky.ModuleName as ModuleName
import Sky.Build.FfiTypeParser (FtyAst(..))


-- | Build a 'Can.Annotation' (i.e. @Forall [tvars] Type@) from an
-- FtyAst, namespacing any unrecognised opaque uppercase identifier
-- under the given kernel name (e.g. @ActionCodeSettings@ from the
-- @Auth@ kernel becomes @TType (Canonical "Auth") "ActionCodeSettings" []@).
--
-- The 'Forall' binder collects every distinct lowercase TVar
-- encountered, including the wildcard @any@. Sky.Type.Solve gives
-- @TVar "any"@ wildcard semantics — distinct occurrences mint fresh
-- unification vars (Limitation #18 fix). For any other lowercase,
-- a single Forall scope per FFI symbol is correct: two occurrences
-- of @a@ in the same skyType (e.g. @a -> Result Error a@) MUST
-- unify, since they're the same parameter / return position.
ftyToAnnotation :: String -> FtyAst -> Can.Annotation
ftyToAnnotation kernelName ast =
    let ty = ftyToType kernelName ast
        tvars = nub (collectTVars ty)
    in Can.Forall tvars ty


-- | Convert FtyAst to a canonical 'Can.Type'. Recognised builtins
-- (Result / Maybe / List / Dict / Task / Set / Error / String /
-- Int / Bool / Float / Char / Bytes) resolve to their canonical
-- module homes via 'Sky.Sky.ModuleName' helpers; everything else
-- becomes an opaque @TType@ namespaced under the FFI kernel.
--
-- Multiple FFI modules can each define their own 'Token' / 'Config'
-- without collision — each kernelName is a separate canonical home,
-- and TType identity is by (home, name, args).
ftyToType :: String -> FtyAst -> Can.Type
ftyToType _kernelName = go
    -- _kernelName is reserved for the proper fix (see opaqueHome
    -- comment): when the inspector starts emitting fully-qualified
    -- Go-package paths in skyType, the kernel name will pin the
    -- canonical home for opaque types referenced there.
  where
    go = \case
        FtyVar name           -> Can.TVar name
        FtyUnit               -> Can.TUnit
        FtyArrow a b          -> Can.TLambda (go a) (go b)
        FtyTuple [t1, t2]     -> Can.TTuple (go t1) (go t2) []
        FtyTuple (t1:t2:rest) -> Can.TTuple (go t1) (go t2) (map go rest)
        FtyTuple _            ->
            -- Single-element tuple is illegal Sky syntax; if it ever
            -- escapes the parser, treat it as a unit fallback rather
            -- than panic.
            Can.TUnit
        FtyApp name args      -> goApp name (map go args)

    goApp :: String -> [Can.Type] -> Can.Type
    goApp name args = case lookup name builtinHome of
        Just home -> Can.TType home name args
        -- v0.17 C17b — qualified opaque types (Cause G).
        -- Inspector-emitted @Customer\@github.com/stripe/stripe-go/v84@
        -- lands here.  C17b parses + carries the marker through;
        -- the resolver currently STILL collapses to @Value@ for
        -- backward compatibility (cross-FFI composition between
        -- a qualified callee and a Value-typed argument breaks
        -- otherwise — the canonical example is a hand-coded
        -- kernel sig like Context.background returning Value
        -- and a Stripe call expecting Customer\@stripe...).
        -- C17c flips the switch once hand-coded kernel sigs
        -- have migrated to the qualified form too.  Until then
        -- the qualified path strips the marker before falling
        -- into the legacy collapse so behaviour is byte-stable.
        Nothing
            | Just (bareName, pkgPath) <- splitQualified name ->
                -- v0.17 PR-21c — SHIP POINT D activation.  The 3
                -- failure modes that broke the 2026-06-13 third
                -- attempt are now closed by safe scaffolding:
                --   * PR-21a (codegen flatten) — solvedTypeToGo
                --     short-circuits the mangled @_at_@ form to
                --     @any@ at 5 renderer sites.  Closes the
                --     @undefined: Router_at_...@ class.
                --   * PR-21b (HM unify axiom) — Sky.Type.Unify's
                --     App1 arm calls @isFfiInterfacePair@ when a
                --     qualified↔qualified pair fails strict
                --     equality.  Closes the Fyne
                --     @Label@/@CanvasObject@ class via Go's
                --     structural interface satisfaction.
                --   * Registry-key mangle in loadAndSeedFfiRegistry
                --     aligns the registry's @\@@-separated keys
                --     with the @_at_@-separated names HM sees, so
                --     the axiom actually fires on the qualified
                --     form.
                --
                -- The flip emits the mangled qualified form
                -- @Bare_at_<pkgmangle>@ as a nullary @Can.TType@
                -- with the empty-home sentinel.  Downstream
                -- consumers see a real qualified name (HM types it
                -- as the qualified-mangled identifier; the
                -- codegen-flatten short-circuit erases it to @any@
                -- at Go emission).
                Can.TType (ModuleName.Canonical "")
                    (bareName ++ "_at_" ++ mangleGoIdent pkgPath)
                    args
            | otherwise -> opaqueValue
      where
        -- Drop @args@ for opaque types — anything generic at the
        -- Go side would have been filtered by isSkyParseable on
        -- the producer; the only remaining shapes are bare opaque
        -- type names and List/Dict/Maybe applied to them. The
        -- latter still resolve to List (Value) etc. because we
        -- recurse through arg positions before reaching here.
        _used = name : map (const "_") args

    -- v0.17 C17b — split a qualified opaque marker (Cause G).
    -- Format: @Name\@pkgPath@.  Returns @Just (name, pkgPath)@ on
    -- match.  Tolerates leading '*' (pointer-of-opaque) — strips
    -- the star before splitting so a pointer-of-Customer still
    -- resolves to the Customer home, only the runtime wrapper
    -- cares about the indirection.
    splitQualified :: String -> Maybe (String, String)
    splitQualified s = case dropWhile (== '*') s of
        bare -> case break (== '@') bare of
            (n, '@':pkg) | not (null n) && not (null pkg) ->
                Just (n, pkg)
            _ -> Nothing

    -- v0.17 C17b — Go-identifier-mangle the package path.
    -- '/' '.' '-' all map to '_' so the result is safe to splice
    -- into any downstream Go identifier emission path. Collisions
    -- between distinct paths are theoretical (would require a path
    -- that differs only in separator chars) — fine for FFI surface.
    mangleGoIdent :: String -> String
    mangleGoIdent = map sanit
      where
        sanit c
            | c == '/' || c == '.' || c == '-' = '_'
            | otherwise                        = c

    -- Unqualified opaque FFI types fall back to the @Value@
    -- sentinel — kept as the safety net for older kernel.json
    -- files (pre-C17a) that don't carry the qualified marker.
    -- The trust-boundary wrap (@Result Error _@) is what HM
    -- actually enforces here.  Per CLAUDE.md "every FFI call
    -- returns Result Error T".  The opaque-type name itself is
    -- decorative when unqualified — runtime wrapper still does the
    -- .(*pkg.X) assertion at the Go boundary, so a wrong opaque
    -- mixed across packages panics at the wrapper with ErrFfi.
    opaqueValue :: Can.Type
    opaqueValue = Can.TType (ModuleName.Canonical "") "Value" []

    -- Closed list of recognised builtin type constructors. Order is
    -- arbitrary — it's a small lookup. Keeping it explicit (rather
    -- than a ModuleName.builtins helper) means an FFI package's
    -- own type accidentally named e.g. @List@ would still resolve
    -- to Sky's List, which is the correct behaviour: HM treats them
    -- as the same type, and the runtime wrapper can do an interface
    -- bridge if needed.
    builtinHome :: [(String, ModuleName.Canonical)]
    builtinHome =
        [ ("String",  ModuleName.basics)
        , ("Int",     ModuleName.basics)
        , ("Bool",    ModuleName.basics)
        , ("Float",   ModuleName.basics)
        , ("Char",    ModuleName.basics)
        , ("Bytes",   ModuleName.basics)
        , ("Error",   ModuleName.Canonical "Sky.Core.Error")
        , ("Result",  ModuleName.result_)
        , ("Maybe",   ModuleName.maybe_)
        , ("List",    ModuleName.list)
        , ("Dict",    ModuleName.dict)
        , ("Set",     ModuleName.set)
        , ("Task",    ModuleName.task)
        ]


-- | Walk a Can.Type collecting every TVar name. Order is
-- deterministic (left-to-right traversal) — 'nub' on the result
-- deduplicates while preserving first-seen order so the Forall
-- binder list mirrors the natural reading order of the type
-- string.
collectTVars :: Can.Type -> [String]
collectTVars = \case
    Can.TVar n        -> [n]
    Can.TUnit         -> []
    Can.TLambda a b   -> collectTVars a ++ collectTVars b
    Can.TTuple a b cs -> collectTVars a ++ collectTVars b ++ concatMap collectTVars cs
    Can.TType _ _ ts  -> concatMap collectTVars ts
    Can.TRecord fs r  ->
        concatMap (collectTVars . Can._fieldType) (Map.elems fs)
            ++ maybe [] (\v -> [v]) r
    Can.TAlias _ _ binders body ->
        concatMap (collectTVars . snd) binders
            ++ collectAliasBody body
  where
    collectAliasBody (Can.Hoisted t) = collectTVars t
    collectAliasBody (Can.Filled  t) = collectTVars t
