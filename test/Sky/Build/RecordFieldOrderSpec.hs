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

import Test.Hspec
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, shell)
import System.Directory (getCurrentDirectory)
import System.FilePath ((</>))
import Data.List (isInfixOf)


spec :: Spec
spec = do
    describe "record auto-ctor honours source field order (audit P0-4)" $ do
        it "generates struct + ctor in declaration order, not alphabetical" $ do
            cwd <- getCurrentDirectory
            let sky = cwd </> "sky-out" </> "sky"
            withSystemTempDirectory "sky-record-order" $ \dir -> do
                -- Fields declared b, a, c — deliberately non-alphabetical
                -- so a broken implementation sorts them into a, b, c and
                -- the test catches it.
                let src = unlines
                        [ "module M exposing (..)"
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
                    fixture = dir </> "M.sky"
                writeFile fixture src
                -- We care about the emitted struct + ctor, not whether
                -- `go build` succeeds — unrelated module-prefix cases
                -- can fail the final link without affecting this
                -- regression. Run the build, ignore its exit code,
                -- and inspect the generated Go directly.
                _ <- readCreateProcessWithExitCode
                    (shell (sky ++ " build " ++ fixture ++ " > /dev/null 2>&1"))
                    ""
                -- sky build writes main.go under cwd/sky-out/
                -- (not inside the tempdir — sky doesn't take an
                -- output dir flag). Read from there.
                goSrc <- readFile (cwd </> "sky-out" </> "main.go")
                -- The entry-module's alias `R` emits as `R_R` (struct
                -- suffix = `_R`). For non-entry dep modules it would
                -- be prefixed (`M_R_R`). Either way, the field order
                -- is what this test guards.
                let structOrder = "type R_R struct {\n\tBeta int\n\tAlpha string\n\tGamma bool\n}"
                (structOrder `isInfixOf` goSrc) `shouldBe` True
                -- Constructor's positional params map to fields in
                -- declaration order (p0→Beta, p1→Alpha, p2→Gamma).
                -- Pre-fix alphabetical would give
                -- `Alpha: ...p0, Beta: ...p1, Gamma: ...p2` — catastrophic
                -- because the user calls `R 99 "hi" True` expecting
                -- beta=99 but would receive alpha=99 (int→string cast
                -- panic).
                let ctorOrder = "Beta: rt.CoerceInt(p0), Alpha: rt.CoerceString(p1), Gamma: rt.CoerceBool(p2)"
                (ctorOrder `isInfixOf` goSrc) `shouldBe` True
