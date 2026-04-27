module Sky.Type.AnyWildcardSpec (spec) where

-- Regression fence for the cross-branch HM `any` wildcard fix
-- (compiler bug #3).
--
-- Pre-fix bug: distinct occurrences of `T.TVar "any"` in source
-- types collapsed to a single fresh unification variable via the
-- solver's `_varCache`. Cross-branch case unification then resolved
-- the shared `any` slot to whatever concrete type appeared first
-- (typically from a sister constructor that didn't use `any`), and
-- subsequent uses at construction sites failed with
-- `Type mismatch: <actual> vs <other-branch's-type>`.
--
-- Fix: in `Sky.Type.Solve.typeToVar`, treat `T.TVar "any"` as a
-- WILDCARD — every occurrence gets its own fresh unification
-- variable, never shared via the cache. This restores the "any
-- unifies with anything, independently" semantics users expect.

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


buildAndRun :: String -> IO (Int, String, String)
buildAndRun src =
    withSystemTempDirectory "sky-any-wild" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"any-wild-test\"\n"
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        let buildOut = bout ++ berr
            bInt = case bec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        if bInt /= 0
            then return (bInt, buildOut, "")
            else do
                let runCmd = "cd " ++ tmp ++ " && ./sky-out/app 2>&1"
                (_, rout, rerr) <- readCreateProcessWithExitCode (shell runCmd) ""
                return (0, buildOut, rout ++ rerr)


spec :: Spec
spec = do
    describe "`any` is a wildcard, not a shared type variable" $ do

        it "ADT with mixed concrete + any-typed branches type-checks across both" $ do
            -- Pre-fix: `case` arm crossing String + any branches
            -- pinned `any` to String, then construction `AttrB 42`
            -- failed with `Int vs String`.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type MyAttr"
                    , "    = AttrA String"
                    , "    | AttrB any"
                    , ""
                    , "toMaybe : MyAttr -> Maybe any"
                    , "toMaybe a ="
                    , "    case a of"
                    , "        AttrA s -> Just s"
                    , "        AttrB v -> Just v"
                    , ""
                    , "main ="
                    , "    case toMaybe (AttrB 42) of"
                    , "        Just _  -> println \"got something\""
                    , "        Nothing -> println \"got nothing\""
                    ]
            (ec, _build, runOut) <- buildAndRun src
            ec `shouldBe` 0
            runOut `shouldSatisfy` ("got something" `isInfixOf`)

        it "same-name `any` in function sig + ctor arg do not share" $ do
            -- Function takes `Maybe any` and produces `any` —
            -- the input `any` and the return `any` must be
            -- independent. Pre-fix they would collapse and
            -- wrap-then-extract chains would fail.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Box = Box any"
                    , ""
                    , "unwrap : Box -> any"
                    , "unwrap b ="
                    , "    case b of"
                    , "        Box v -> v"
                    , ""
                    , "main ="
                    , "    let"
                    , "        s = unwrap (Box \"hello\")"
                    , "        n = unwrap (Box 42)"
                    , "        _ = println \"both unwraps type-checked\""
                    , "    in"
                    , "        println \"done\""
                    ]
            (ec, _, runOut) <- buildAndRun src
            ec `shouldBe` 0
            runOut `shouldSatisfy` ("both unwraps type-checked" `isInfixOf`)

        it "two ctor args both `any` get independent unification slots" $ do
            -- Both fields of the constructor are `any` — they must
            -- NOT share a type slot. Pre-fix the two `any` in
            -- `Pair any any` collapsed and the second arg was
            -- forced to match the first.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Pair = Pair any any"
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = Pair \"hello\" 42"
                    , "        _ = Pair 1 \"world\""
                    , "    in"
                    , "        println \"two-any-args ok\""
                    ]
            (ec, _, runOut) <- buildAndRun src
            ec `shouldBe` 0
            runOut `shouldSatisfy` ("two-any-args ok" `isInfixOf`)
