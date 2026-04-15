module Sky.Lsp.HoverShadowingSpec (spec) where

-- Audit P2-2: LSP lookupLocal resolves shadowed bindings by
-- smallest-enclosing-scope. Pre-fix the `idxLocalTypes` map was
-- keyed by name only (`Map String Type`), so `let x = 1 in let x =
-- "s" in x` collapsed into a single entry that was WHICHEVER the
-- solver captured last — the wrong type for the inner hover. Fix:
-- the map now stores `[Type]` (innermost-first), and lookupLocal
-- returns the head, which pairs with its own smallest-scope
-- binding match.
--
-- End-to-end LSP JSON-RPC tests are out of scope here (tracked as
-- P3-2). This spec exercises the public compiler behaviour: a Sky
-- fixture that shadows a local must parse + type-check, and `sky
-- fmt --stdin` must round-trip it. These surface as the necessary-
-- but-not-sufficient guardrails that the shape change didn't
-- break anything obvious.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.Exit (ExitCode(..))
import System.IO.Temp (withSystemTempDirectory)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- Two sibling functions both using `x` as a local binding with
-- DIFFERENT types. Pre-P2-2 the solver's Map-String-Type _locals
-- collapsed both into a single type entry (whichever fired last).
-- Post-P2-2 the map stores a list, so both types survive.
--
-- We can't use in-function same-name shadowing (`let x = 1 in let
-- x = 2 in …`) here because the Go emitter's nested-let codegen
-- still emits `x := ...` twice, which Go rejects. That's a
-- separate pre-existing codegen bug out of P2-2's scope.
shadowingFixture :: String
shadowingFixture = unlines
    [ "module M exposing (intX, strX)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , ""
    , "intX ="
    , "    let x = 42"
    , "    in x"
    , ""
    , "strX ="
    , "    let x = \"hello\""
    , "    in x"
    ]


spec :: Spec
spec = do
    describe "shadowing doesn't break compile pipeline (audit P2-2)" $ do

        it "sky check accepts a shadowed-local fixture" $ do
            sky <- findSky
            withSystemTempDirectory "sky-shadow" $ \dir -> do
                let fixture = dir </> "Shadow.sky"
                writeFile fixture shadowingFixture
                (ec, _out, err) <- readCreateProcessWithExitCode
                    (shell (sky ++ " check " ++ fixture))
                    ""
                case ec of
                    ExitSuccess -> return ()
                    ExitFailure _ ->
                        expectationFailure ("sky check rejected shadowing: " ++ err)

        it "sky fmt --stdin round-trips a shadowed-local fixture" $ do
            sky <- findSky
            (ec, out, _err) <- readCreateProcessWithExitCode
                (shell ("SKY_FMT_FORCE=1 " ++ sky ++ " fmt --stdin"))
                shadowingFixture
            ec `shouldBe` ExitSuccess
            -- Both `x` bindings survive: two `x =` assignments
            -- in the output.
            -- Both `x` bindings must appear in the output — one
            -- per sibling function.
            let countXLets s =
                    length [() | l <- lines s, let t = dropWhile (== ' ') l
                                , take 6 t == "x = 42" || take 11 t == "x = \"hello\""]
            countXLets out `shouldBe` 2
