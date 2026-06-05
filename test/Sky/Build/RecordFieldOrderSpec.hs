module Sky.Build.RecordFieldOrderSpec (spec) where

-- Audit P0-4: the auto-generated record constructor's positional
-- parameter order is the user's source declaration order, NOT the
-- alphabetical order that falls out of Map.toList on a Map-keyed
-- field registry. Pre-fix, `type alias Piece = { kind : Kind,
-- colour : Colour }` emitted a Go constructor with (colour, kind)
-- parameter order because Map.keys is alphabetical. User code like
-- `Piece King White` then panicked at the `.(Colour)` type-assert
-- in the generated Go struct literal.
--
-- The fix sorts Map.toList output by the FieldType's _fieldIndex
-- (which the canonicaliser populates with the source-position index)
-- at all three emission sites. This spec locks the invariant in.
--
-- v0.15.52 #396 — workdir isolation. Pre-fix the spec invoked
-- `sky build` from the repo cwd and then read the emitted
-- `<cwd>/sky-out/main.go`. That races CheckIsBuildSpec (and any
-- other spec touching the in-tree sky-out/), which can wipe or
-- partially overwrite the file mid-read; on parallel runs the
-- `readFile` then fires `NoSuchThing: openFile: does not exist`.
-- Same shape as the #381 ExampleSweep fix: invoke `sky build` with
-- `cwd = $TMPDIR/sky-rfo-…` so emit lands in the tempdir and the
-- spec reads from there.
--
-- Tier 1 (task #491): no subprocess `sky build` — the compile
-- pipeline runs IN-PROCESS via Sky.Build.Helpers.InProcessCompile.
-- Workdir isolation is now structural (each call uses a fresh
-- tempdir + ZERO shared sky-out/) and there's no cwd-race surface.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = do
    describe "record auto-ctor honours source field order (audit P0-4)" $ do
        it "generates struct + ctor in declaration order, not alphabetical" $ do
            -- Fields declared b, a, c — deliberately non-alphabetical
            -- so a broken implementation sorts them into a, b, c and
            -- the test catches it.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , ""
                    , "type alias R ="
                    , "    { beta : Int"
                    , "    , alpha : String"
                    , "    , gamma : Bool"
                    , "    }"
                    , ""
                    , "sample : R"
                    , "sample = R 99 \"hi\" True"
                    , ""
                    , "-- Force the function into the reachable graph."
                    , "main = sample"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk goSrc -> do
                    -- The entry-module's alias `R` emits as `R_R`
                    -- (struct suffix = `_R`). For non-entry dep
                    -- modules it would be prefixed (`M_R_R`). Either
                    -- way, the field order is what this test guards.
                    let structOrder = "type R_R struct {\n\tBeta int\n\tAlpha string\n\tGamma bool\n}"
                    (structOrder `isInfixOf` goSrc) `shouldBe` True
                    -- Constructor's positional params map to fields in
                    -- declaration order (p0→Beta, p1→Alpha, p2→Gamma).
                    -- Pre-fix alphabetical would give
                    -- `Alpha: ...p0, Beta: ...p1, Gamma: ...p2` —
                    -- catastrophic because the user calls
                    -- `R 99 "hi" True` expecting beta=99 but would
                    -- receive alpha=99 (int→string cast panic).
                    -- With typed-codegen, the ctor params are
                    -- concretely typed (int/string/bool) so
                    -- rt.CoerceX isn't needed; accept either the
                    -- coerced or raw form, but still assert the
                    -- FIELD ORDER is Beta→Alpha→Gamma.
                    let ctorTyped = "Beta: p0, Alpha: p1, Gamma: p2"
                        ctorCoerced = "Beta: rt.CoerceInt(p0), Alpha: rt.CoerceString(p1), Gamma: rt.CoerceBool(p2)"
                    ((ctorTyped `isInfixOf` goSrc) || (ctorCoerced `isInfixOf` goSrc))
                        `shouldBe` True
