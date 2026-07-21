module Sky.Build.AnonLambdaSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- v0.13 D-Lambda-Lowerer regression suite.
--
-- Sky lambda expressions (`\x -> ...`) lower differently depending on
-- the param slot they're passed to. Before D-Lambda-Lowerer (this
-- session, May 2026), only KERNEL HOF call sites (Result.andThen,
-- Maybe.andThen, List.map, …) routed literal lambdas through
-- `curryLambdaPatTyped`. User-defined HOFs fell back to the untyped
-- `func(any) any` shape, which Go's call-site inference then
-- rejected at any typed-func slot.
--
-- D-Lambda-Lowerer wires user-defined HOFs through the same typed
-- lambda path via `coerceCallArgsAt`'s no-CSI fallback (a
-- `Can.Lambda` arm in `coerceFallback`). These tests pin the
-- emitted Go shape so a future regression to the untyped path
-- trips at cabal-test time.
spec :: Spec
spec = do
    describe "user-defined HOF with Result-typed lambda" $ do
        it "emits typed `func(any) rt.SkyResult[E, V]` HOF param sig" $ do
            sky <- findSky
            withSystemTempDirectory "sky-anon-lambda-do" $ \tmp -> do
                writeFixture tmp doFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- The `do` helper's `fn` param renders the typed
                -- return slot. Pre-D1 it was `func(any) any`.
                ("rt.SkyResult[Sky_Core_Error_Error" `isInfixOf` body)
                    `shouldBe` True
                -- The do() emission must include the typed sig —
                -- search for `func do[` (generic) with rt.SkyResult
                -- in the fn param.
                (("func do[" `isInfixOf` body)
                    || ("func Sky_Main_do[" `isInfixOf` body)
                    || ("func do(" `isInfixOf` body)
                    || ("func Sky_Main_do(" `isInfixOf` body))
                    `shouldBe` True

        it "emits the lambda body with typed return (rt.ResultCoerce wrap)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-anon-lambda-coerce" $ \tmp -> do
                writeFixture tmp doFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- The lambda body wraps its return through
                -- `rt.ResultCoerce[Sky_Core_Error_Error, ...]` so
                -- the Sky lambda's `func(any) any` body shape
                -- bridges to the typed sig.
                ("rt.ResultCoerce[Sky_Core_Error_Error" `isInfixOf` body)
                    `shouldBe` True

        it "lambda param emits typed Go param, not bare `any` shape" $ do
            sky <- findSky
            withSystemTempDirectory "sky-anon-lambda-param" $ \tmp -> do
                writeFixture tmp doFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- Sky lambdas always have `func(a any)` for their
                -- input — the typed-lowerer's win is the typed
                -- RETURN. Old `func(a any) any` shape must be
                -- replaced with `func(a any) rt.SkyResult[...]`.
                -- We assert there's NO `func(a any) any {` for `a`
                -- — the param-`a` lambdas in `do (...) (\a -> ...)`
                -- must be typed.
                not ("func(a any) any {" `isInfixOf` body)
                    `shouldBe` True


    describe "Msg-typed callback to a user-defined HOF" $ do
        it "emits the helper sig with `cb func(string) Msg` typed return" $ do
            sky <- findSky
            withSystemTempDirectory "sky-anon-lambda-msg" $ \tmp -> do
                writeFixture tmp msgCtorFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- The `field` helper's `cb` param renders the
                -- typed return as `Msg` (D1). Pre-D1 it was `any`.
                ("cb func(string) Msg" `isInfixOf` body) `shouldBe` True

        it "passes Msg ctor RAW (no rt.Coerce wrap) post σ-pinning" $ do
            -- v0.13 Stage 1 update: with ADT-ctor sigs registered in
            -- `_cg_funcParamTypes` and σ-pinning preserving TVars in
            -- the substituteOnly path, the call-site `cb` typed slot
            -- (`func(string) Msg`) matches the Msg ctor's own sig
            -- (`func(string) Msg`) directly, so no `rt.Coerce` wrap
            -- is needed. Closing this adapter class is the entire
            -- point of the Stage 1 commit chain.
            sky <- findSky
            withSystemTempDirectory "sky-anon-lambda-raw-msg" $ \tmp -> do
                writeFixture tmp msgCtorFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- The wrap MUST be absent; Msg_UserChanged flows raw.
                ("rt.Coerce[func(string) Msg](Msg_UserChanged)"
                    `isInfixOf` body) `shouldBe` False


    describe "Maybe-typed lambda at a user-defined HOF" $ do
        it "emits typed `rt.SkyMaybe[V]` for the helper return slot" $ do
            sky <- findSky
            withSystemTempDirectory "sky-anon-lambda-maybe" $ \tmp -> do
                writeFixture tmp maybeFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- The `firstJust` helper's `fn` param renders with
                -- typed Maybe return shape (D1).
                ("rt.SkyMaybe[" `isInfixOf` body) `shouldBe` True


  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir = do
        let cp = (proc sky args) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    writeFixture :: FilePath -> String -> IO ()
    writeFixture dir body = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml")
            ("name = \"anon-lambda\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") body


-- ─── Fixtures ──────────────────────────────────────────────────────

-- User-defined `do` HOF on Result. Classic monadic-do pattern.
doFixture :: String
doFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Result as Result"
    , "import Sky.Core.Error as Error exposing (Error)"
    , "import Std.Log exposing (println)"
    , ""
    , "do : Result Error a -> (a -> Result Error b) -> Result Error b"
    , "do result fn ="
    , "    Result.andThen fn result"
    , ""
    , "pipeline : String -> Result Error String"
    , "pipeline key ="
    , "    do (firstStep key) (\\a ->"
    , "    do (secondStep a) (\\b ->"
    , "    Ok (a ++ \"-\" ++ b)))"
    , ""
    , "firstStep : String -> Result Error String"
    , "firstStep key ="
    , "    if key == \"\" then Err (Error.invalidInput \"empty\")"
    , "    else Ok (\"first:\" ++ key)"
    , ""
    , "secondStep : String -> Result Error String"
    , "secondStep a = Ok (\"second:\" ++ a)"
    , ""
    , "main ="
    , "    case pipeline \"hello\" of"
    , "        Ok s -> println (\"ok \" ++ s)"
    , "        Err e -> println (errorToString e)"
    ]


-- User-defined HOF receiving a typed `(String -> Msg)` callback.
msgCtorFixture :: String
msgCtorFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "type Msg = UserChanged String | PassChanged String"
    , ""
    , "field : String -> (String -> Msg) -> Msg"
    , "field initial cb = cb initial"
    , ""
    , "main ="
    , "    let m = field \"alice\" UserChanged"
    , "    in case m of"
    , "        UserChanged s -> println (\"user: \" ++ s)"
    , "        PassChanged s -> println (\"pass: \" ++ s)"
    ]


-- User-defined HOF returning Maybe. Lambda return type is concrete.
maybeFixture :: String
maybeFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Maybe as Maybe"
    , "import Std.Log exposing (println)"
    , ""
    , "firstJust : Maybe a -> (a -> Maybe b) -> Maybe b"
    , "firstJust m fn = Maybe.andThen fn m"
    , ""
    , "chain : Int -> Maybe String"
    , "chain n ="
    , "    firstJust (positive n) (\\p ->"
    , "    firstJust (doubled p) (\\d ->"
    , "    Just (\"value:\" ++ String.fromInt d)))"
    , ""
    , "positive : Int -> Maybe Int"
    , "positive n = if n > 0 then Just n else Nothing"
    , ""
    , "doubled : Int -> Maybe Int"
    , "doubled n = Just (n * 2)"
    , ""
    , "main ="
    , "    case chain 21 of"
    , "        Just s -> println s"
    , "        Nothing -> println \"none\""
    ]
