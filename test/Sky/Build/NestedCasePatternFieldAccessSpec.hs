module Sky.Build.NestedCasePatternFieldAccessSpec (spec) where

-- Regression fence for typed record field access on a value bound
-- through a nested case pattern.
--
-- Pre-fix bug (v0.16.16 and earlier): when a typed record value is
-- destructured through two layers of case patterns like
-- `case x of Ok (Ok b) -> b.field`, the Sky compiler erases the
-- typed shape of `b` and emits `b := rt.ResultOk(any(...))` (any-
-- typed). Subsequent `b.field` accesses then lower to
-- `rt.Field(b, "Field")` reflective lookup, which returns the Go
-- zero-value (Int 0, String "", nil pointer, etc.) instead of the
-- actual data — silently. No panic; just junk data.
--
-- This is a SOUNDNESS bug, not just a correctness bug: the type
-- system says `b : Box` so `b.field` should have type `Box.field`,
-- but the runtime returns a different value. No error is raised.
--
-- Real-world impact: SkyDeploy's MCP server's
-- `case Task.run (verifyMcpToken s) of Ok (Ok vt) -> vt.userId`
-- read userId=0 instead of the actual user id, breaking every
-- per-user query (list_apps, get_app, etc).
--
-- Sibling bug class: #461 / #463 / #465 / #467 (typed-record-alias
-- panics through any-typed wrappers). Those manifested as
-- rt.Coerce panics; this one fails silently with zero-values.
--
-- The fix preserves the typed record shape through nested case
-- pattern destructuring. The emitted Go for `Ok (Ok b) ->` now
-- includes a `rt.Coerce[Box_R]` wrap around the inner extraction
-- so `b` keeps its concrete type and `b.UserId` reads directly
-- from the struct (no reflect).

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.IO.Temp (withSystemTempDirectory)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- Build + run a single Sky source file in a fresh temp directory and
-- return (exit code, combined stdout+stderr from sky build, stdout
-- from running the binary).
buildAndRun :: String -> IO (Int, String, String)
buildAndRun src =
    withSystemTempDirectory "sky-nested-pattern" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"nested-pattern-test\"\n"
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
    describe "Nested case-pattern field access preserves typed shape" $ do

        it "Result (Result Box) — Ok (Ok b) -> b.field reads correct values" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Box ="
                    , "    { userId : Int"
                    , "    , label : String"
                    , "    }"
                    , ""
                    , "readBoxed : Result String (Result String Box) -> String"
                    , "readBoxed input ="
                    , "    case input of"
                    , "        Ok (Ok b) -> \"userId=\" ++ String.fromInt b.userId ++ \" label=\" ++ b.label"
                    , "        Ok (Err e) -> \"inner-err: \" ++ e"
                    , "        Err e -> \"outer-err: \" ++ e"
                    , ""
                    , "boxed : Result String (Result String Box)"
                    , "boxed = Ok (Ok { userId = 42, label = \"hello\" })"
                    , ""
                    , "main = println (readBoxed boxed)"
                    ]
            (bec, buildOut, runOut) <- buildAndRun src
            bec `shouldBe` 0
            -- BUG (pre-fix): runOut contains "userId=0 label=" (zero-values)
            -- FIX: runOut contains "userId=42 label=hello"
            runOut `shouldContain` "userId=42 label=hello"
            buildOut `shouldNotContain` "panic"

        it "Maybe (Maybe Box) — Just (Just b) -> b.field also typed" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Box ="
                    , "    { value : Int }"
                    , ""
                    , "readBoxed : Maybe (Maybe Box) -> String"
                    , "readBoxed input ="
                    , "    case input of"
                    , "        Just (Just b) -> \"value=\" ++ String.fromInt b.value"
                    , "        _ -> \"missing\""
                    , ""
                    , "boxed : Maybe (Maybe Box)"
                    , "boxed = Just (Just { value = 7 })"
                    , ""
                    , "main = println (readBoxed boxed)"
                    ]
            (bec, buildOut, runOut) <- buildAndRun src
            bec `shouldBe` 0
            runOut `shouldContain` "value=7"
            buildOut `shouldNotContain` "panic"

        it "String-typed field (UUID-like) also reads correctly through nested pattern" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Session ="
                    , "    { sid : String"
                    , "    , uid : String"
                    , "    }"
                    , ""
                    , "readSession : Result String (Result String Session) -> String"
                    , "readSession input ="
                    , "    case input of"
                    , "        Ok (Ok s) -> s.sid ++ \"/\" ++ s.uid"
                    , "        _ -> \"missing\""
                    , ""
                    , "session : Result String (Result String Session)"
                    , "session = Ok (Ok { sid = \"abc-123\", uid = \"user-456\" })"
                    , ""
                    , "main = println (readSession session)"
                    ]
            (bec, buildOut, runOut) <- buildAndRun src
            bec `shouldBe` 0
            -- BUG (pre-fix): runOut contains "/" (both empty strings)
            -- FIX: runOut contains "abc-123/user-456"
            runOut `shouldContain` "abc-123/user-456"
            buildOut `shouldNotContain` "panic"
