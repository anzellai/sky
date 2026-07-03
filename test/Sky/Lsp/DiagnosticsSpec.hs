{-# LANGUAGE OverloadedStrings #-}
module Sky.Lsp.DiagnosticsSpec (spec) where

-- LSP diagnostic-parity specs. The editor must see every error
-- `sky check` / `sky build` sees — anything less is a developer-
-- experience regression. This file asserts the signal flows through
-- `textDocument/publishDiagnostics` for:
--
--   * non-exhaustive case expressions (Gap 2a)
--   * undefined/unbound names (Gap 2b, depends on Gap 1)
--
-- Infrastructure note: `awaitNotification` in Sky.Lsp.Harness waits
-- for the server-pushed diagnostic (no request/response correlation).
-- Pre-harness, ProtocolSpec/CapabilitiesSpec discarded such
-- notifications while draining for a response.

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson (Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.Text as T
import qualified Data.Vector as V
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)

import Sky.Lsp.Harness
    ( findSky, withLsp
    , initializeLsp, didOpen, didSave
    , awaitNotification
    )


setupProject :: FilePath -> String -> IO FilePath
setupProject dir src = do
    let srcDir = dir </> "src"
        fixture = srcDir </> "Main.sky"
        toml = dir </> "sky.toml"
    createDirectoryIfMissing True srcDir
    writeFile toml "name = \"lsp-diag\"\nentry = \"src/Main.sky\"\n"
    writeFile fixture src
    return fixture


-- | Extract the list of diagnostic message strings from a
-- publishDiagnostics notification payload.
diagnosticMessages :: Aeson.Value -> [T.Text]
diagnosticMessages v = case v of
    Object o -> case KM.lookup "params" o of
        Just (Object p) -> case KM.lookup "diagnostics" p of
            Just (Array arr) -> concatMap getMsg (V.toList arr)
            _ -> []
        _ -> []
    _ -> []
  where
    getMsg (Object d) = case KM.lookup "message" d of
        Just (String t) -> [t]
        _ -> []
    getMsg _ = []


-- | Extract the list of `code` fields from a publishDiagnostics
-- notification payload.  v0.13 Layer 4: every LSP diagnostic
-- carries the same stable code the CLI emits, so editor
-- extensions can filter / link by code.
diagnosticCodes :: Aeson.Value -> [T.Text]
diagnosticCodes v = case v of
    Object o -> case KM.lookup "params" o of
        Just (Object p) -> case KM.lookup "diagnostics" p of
            Just (Array arr) -> concatMap getCode (V.toList arr)
            _ -> []
        _ -> []
    _ -> []
  where
    getCode (Object d) = case KM.lookup "code" d of
        Just (String t) -> [t]
        _ -> []
    getCode _ = []


anyMatch :: T.Text -> [T.Text] -> Bool
anyMatch needle = any (needle `T.isInfixOf`)


spec :: Spec
spec = do
    describe "LSP publishes diagnostics for every Sky-level error" $ do

        it "Gap 2a — non-exhaustive case surfaces as an editor diagnostic" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Colour = Red | Green | Blue"
                    , ""
                    , "name c ="
                    , "    case c of"
                    , "        Red -> \"red\""
                    , "        Green -> \"green\""
                    , ""
                    , "main = println (name Red)"
                    ]
            withSystemTempDirectory "sky-lsp-diag-exhaust" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    -- Server pushes publishDiagnostics after didOpen.
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            anyMatch "Non-exhaustive" msgs `shouldBe` True
                            -- The missing constructor should be named.
                            anyMatch "Blue" msgs `shouldBe` True

        it "Gap 2b — undefined name surfaces as an editor diagnostic" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println messgae"
                    ]
            withSystemTempDirectory "sky-lsp-diag-unbound" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            anyMatch "Undefined name" msgs `shouldBe` True
                            anyMatch "messgae" msgs `shouldBe` True

        it "issue #52 — partial application at stdlib boundary produces a diagnostic" $ do
            -- Regression for issue #52
            -- (https://github.com/anzellai/sky/issues/52): a Sky.Live
            -- view function calling `Ui.layout [] (codeSection)` —
            -- where codeSection : Model -> Element — should be flagged
            -- by the LSP. Pre-fix, this compiled (and crashed at
            -- runtime) AND the LSP showed no red squiggles. The fix
            -- (cross-module externals threaded into runPipelineSt)
            -- makes HM see Ui.layout's signature, so the partial
            -- application surfaces as a real type-error diagnostic.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , "import Std.Ui as Ui"
                    , "import Std.Ui exposing (Element)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , ""
                    , "codeSection : Model -> Element msg"
                    , "codeSection model ="
                    , "    Ui.text (String.fromInt model.count)"
                    , ""
                    , "viewBuggy : Model -> any"
                    , "viewBuggy model ="
                    , "    Ui.layout [] codeSection"
                    , ""
                    , "main = println (toString (viewBuggy { count = 0 }))"
                    ]
            withSystemTempDirectory "sky-lsp-issue52" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            -- The diagnostic should mention the
                            -- mis-applied identifier and the type
                            -- mismatch. Phrasing: "Foreign 'Main.codeSection':
                            -- (Model) -> Element a vs Element a"
                            anyMatch "codeSection" msgs `shouldBe` True
                            anyMatch "Element" msgs `shouldBe` True

        it "Limitation #19 — Tui.app missing required field surfaces a diagnostic" $ do
            -- Closed-record HM kernel sig (commit follows this test)
            -- enforces presence of init / update / view / subscriptions
            -- on Tui.app's cfg. A missing field used to compile silently
            -- and panic at runtime — now both `sky check` and the LSP
            -- flag it at type-check time. This test omits
            -- `subscriptions` to assert the diagnostic surfaces.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Task as Task"
                    , "import Std.Cmd as Cmd"
                    , "import Std.Tui as Tui"
                    , "import Std.Ui as Ui"
                    , "import Std.Ui exposing (Element)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , "type Msg = NoOp"
                    , ""
                    , "init : a -> ( Model, Cmd Msg )"
                    , "init _ = ( { count = 0 }, Cmd.none )"
                    , ""
                    , "update : Msg -> Model -> ( Model, Cmd Msg )"
                    , "update _ model = ( model, Cmd.none )"
                    , ""
                    , "view : Model -> Element Msg"
                    , "view _ = Ui.text \"hi\""
                    , ""
                    , "main ="
                    , "    Tui.app"
                    , "        { init = init"
                    , "        , update = update"
                    , "        , view = view"
                    , "        }"
                    , "        |> Task.run"
                    ]
            withSystemTempDirectory "sky-lsp-tui-missing" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on missing-field file"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            -- Diagnostic should mention the missing
                            -- field explicitly. Sky's record-mismatch
                            -- renderer lists every field the user
                            -- gave AND every field expected, so
                            -- "subscriptions" appears in the
                            -- expected-shape list.
                            anyMatch "Type" msgs `shouldBe` True
                            anyMatch "subscriptions" msgs `shouldBe` True

        it "issue #52 corrected — applying the missing arg clears the diagnostic" $ do
            -- Positive control for the fix above: change `codeSection`
            -- to `codeSection model` and the LSP should report empty
            -- diagnostics. Without this, a false-positive could leave
            -- the squiggle on every save indefinitely.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , "import Std.Ui as Ui"
                    , "import Std.Ui exposing (Element)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , ""
                    , "codeSection : Model -> Element msg"
                    , "codeSection model ="
                    , "    Ui.text (String.fromInt model.count)"
                    , ""
                    , "viewCorrect : Model -> any"
                    , "viewCorrect model ="
                    , "    Ui.layout [] (codeSection model)"
                    , ""
                    , "main = println (toString (viewCorrect { count = 0 }))"
                    ]
            withSystemTempDirectory "sky-lsp-issue52-fix" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "clean file produces a diagnostics notification with an empty array" $ do
            -- Positive control: a valid file should still trigger
            -- publishDiagnostics (empty), so editors that cache
            -- diagnostics clear stale state.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"hi\""
                    ]
            withSystemTempDirectory "sky-lsp-diag-clean" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on clean file"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "user-defined monadic-do chain on Result produces no diagnostics" $ do
            -- Regression for Bug #1: previously `sky check` passed but
            -- `go build` failed on this pattern. LSP piggy-backs on the
            -- sky-check pipeline (Parse → Canonicalise → Constrain →
            -- Solve → Exhaustiveness), so a false-positive LSP
            -- diagnostic here would mean the type solver was spuriously
            -- rejecting a valid user-HOF chain. Empty diagnostics =>
            -- LSP agrees with the fixed codegen.
            sky <- findSky
            let src = unlines
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
                    , "pipeline : String -> Result Error (String, String)"
                    , "pipeline key ="
                    , "    do (firstStep key) (\\a ->"
                    , "    do (secondStep a) (\\b ->"
                    , "    Ok (a, b)))"
                    , ""
                    , "firstStep : String -> Result Error String"
                    , "firstStep key ="
                    , "    if key == \"\" then"
                    , "        Err (Error.invalidInput \"empty key\")"
                    , "    else"
                    , "        Ok (\"first:\" ++ key)"
                    , ""
                    , "secondStep : String -> Result Error String"
                    , "secondStep a ="
                    , "    Ok (\"second:\" ++ a)"
                    , ""
                    , "main ="
                    , "    case pipeline \"hello\" of"
                    , "        Ok (a, b) ->"
                    , "            println (\"ok \" ++ a ++ \" \" ++ b)"
                    , ""
                    , "        Err e ->"
                    , "            println (errorToString e)"
                    ]
            withSystemTempDirectory "sky-lsp-diag-user-hof" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on user-HOF chain"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "TEA with Live.app: wrong view return type surfaces as a real diagnostic" $ do
            -- Pre-Limitation-#19-fix this was a "the LSP suppresses
            -- false positives" test. With the closed-record HM kernel
            -- sigs landed (Live.app / Tui.app / Cli.program) PLUS
            -- the field-name-aware error rendering, a wrong-typed
            -- view function is now both:
            --   1. Rejected by `sky check` (always was — the user
            --      kernel sig had this; the LSP was hiding it).
            --   2. Rejected by the LSP with the SAME informative
            --      diagnostic, listing the expected view return
            --      type (Html Msg — v0.13 Layer 3 migrated
            --      Live.app's `view` field to the Sky-source
            --      `Html msg` ADT) vs what the user wrote (String).
            -- This test fixture deliberately writes `view _ = "hi"`
            -- to assert the diagnostic surfaces.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Cmd as Cmd"
                    , "import Std.Sub as Sub"
                    , "import Std.Live exposing (app)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , "type Msg = Tick"
                    , ""
                    , "init : a -> ( Model, Cmd Msg )"
                    , "init _ ="
                    , "    ( { count = 0 }, Cmd.none )"
                    , ""
                    , "update : Msg -> Model -> ( Model, Cmd Msg )"
                    , "update msg model ="
                    , "    ( model, Cmd.none )"
                    , ""
                    , "view : Model -> any"
                    , "view _ = \"hi\""
                    , ""
                    , "subscriptions _ = Sub.none"
                    , ""
                    , "main ="
                    , "    app"
                    , "        { init = init"
                    , "        , update = update"
                    , "        , view = view"
                    , "        , subscriptions = subscriptions"
                    , "        , routes = []"
                    , "        , notFound = ()"
                    , "        }"
                    ]
            withSystemTempDirectory "sky-lsp-tea-app" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on TEA-app file"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            -- The diagnostic surfaces with field-
                            -- aware rendering — `view`'s return is
                            -- `String` where `Html Msg` is expected.
                            -- (v0.13 Layer 3: Live.app's `view` field
                            -- returns the Sky-source `Html msg` ADT,
                            -- not the old runtime `VNode`.)
                            anyMatch "Type" msgs `shouldBe` True
                            anyMatch "view" msgs `shouldBe` True
                            anyMatch "Html" msgs `shouldBe` True

        it "cross-module record-alias usage is not flagged as a type error" $ do
            -- Regression for the v0.13.4 LSP false-positive surfaced
            -- by examples/19-skyforum + mini-notion: when a buffer
            -- references a record alias from another module
            -- (`Db.Page`) AND accesses its fields (`page.id`), the
            -- LSP previously reported `Page vs { id : a | ...}` even
            -- though `sky check` was clean. Same diagnostics file
            -- also flagged unqualified cross-module type references
            -- (`Model` in `viewLive : Model -> any` when Model is
            -- defined in src/Model.sky) as `Model vs Model` (same
            -- name, different identity).
            --
            -- Root cause: `runPipelineSt` called
            -- `Canonicalise.canonicalise srcMod` (no deps). Without
            -- dep info, cross-module record-alias references stayed
            -- as nominal `TType` instead of being expanded to
            -- `TAlias` (so `.id` field access couldn't unify), and
            -- unqualified type names resolved to empty-home
            -- `Canonical ""` while the externals-side reference
            -- (built by the workspace pipeline, which DID have full
            -- deps) resolved to `Canonical "Model"` — HM saw two
            -- distinct identities.
            --
            -- Fix: thread `Idx.depInfoFromIndex idx` into a
            -- `canonicaliseWithDeps` call so the buffer's
            -- canonicalisation matches the CLI pipeline. This test
            -- asserts EMPTY diagnostics on a multi-file project
            -- whose Main.sky exhibits both shapes of the bug.
            sky <- findSky
            let dbSrc = unlines
                    [ "module Db exposing (Page, getPage)"
                    , ""
                    , "type alias Page ="
                    , "    { id : Int"
                    , "    , title : String"
                    , "    , content : String"
                    , "    }"
                    , ""
                    , "getPage : Int -> Page"
                    , "getPage n ="
                    , "    { id = n, title = \"\", content = \"\" }"
                    ]
                modelSrc = unlines
                    [ "module Model exposing (Model, initial)"
                    , ""
                    , "type alias Model ="
                    , "    { count : Int"
                    , "    , name : String"
                    , "    }"
                    , ""
                    , "initial : Model"
                    , "initial = { count = 0, name = \"\" }"
                    ]
                viewSrc = unlines
                    [ "module View exposing (titleOf, pageIdOf)"
                    , ""
                    , "import Db"
                    , ""
                    -- Cross-module record-alias usage: parameter
                    -- annotated `Db.Page` (a nominal TType pre-fix)
                    -- accessing `.id` (requires TAlias expansion).
                    , "pageIdOf : Db.Page -> Int"
                    , "pageIdOf page ="
                    , "    page.id"
                    , ""
                    , "titleOf : Db.Page -> String"
                    , "titleOf page ="
                    , "    page.title"
                    ]
                mainSrc = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , "import Model exposing (..)"
                    , "import View"
                    , "import Db"
                    , ""
                    -- Unqualified cross-module type reference
                    -- (`Model`): pre-fix resolved to empty-home in
                    -- this buffer, but its externals-side counter-
                    -- part resolved to home="Model" — "Model vs
                    -- Model" mismatch on identity, not display.
                    , "render : Model -> String"
                    , "render model ="
                    , "    \"count: \" ++ String.fromInt model.count"
                    , ""
                    -- And cross-module use of View.pageIdOf, which
                    -- itself depends on Db.Page alias expansion.
                    , "main ="
                    , "    let"
                    , "        m = initial"
                    , "        p = Db.getPage 42"
                    , "        _ = println (render m)"
                    , "    in"
                    , "        println (String.fromInt (View.pageIdOf p))"
                    ]
            withSystemTempDirectory "sky-lsp-cross-mod-alias" $ \dir -> do
                let srcDir = dir </> "src"
                createDirectoryIfMissing True srcDir
                writeFile (dir </> "sky.toml")
                    "name = \"lsp-cross-alias\"\nentry = \"src/Main.sky\"\n"
                writeFile (srcDir </> "Db.sky") dbSrc
                writeFile (srcDir </> "Model.sky") modelSrc
                writeFile (srcDir </> "View.sky") viewSrc
                let mainPath = srcDir </> "Main.sky"
                writeFile mainPath mainSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    -- Open Main.sky (the file with BOTH bug shapes:
                    -- unqualified cross-module type + transitive
                    -- reliance on View.pageIdOf which itself uses
                    -- Db.Page alias expansion).
                    didOpen hin mainPath mainSrc
                    resMain <- awaitNotification hout
                        "textDocument/publishDiagnostics"
                    case resMain of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on Main.sky"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []
                    -- Then open View.sky (cross-module record-alias
                    -- field access only).
                    let viewPath = srcDir </> "View.sky"
                    didOpen hin viewPath viewSrc
                    resView <- awaitNotification hout
                        "textDocument/publishDiagnostics"
                    case resView of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on View.sky"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "dual-import qualifier collision surfaces with merge-into-alias fix-it" $ do
            -- PR follow-up to task #347 (D5). The canonicaliser diagnostic for
            -- E1001 dual-import collisions was updated to detect a pre-existing
            -- `import M as X` line for one of the colliding modules and suggest
            -- MERGING the exposing list into that line, instead of naively
            -- proposing another alias. This test pins the improved wording at
            -- the LSP layer — publishDiagnostics is a straight string pipe from
            -- canonicaliser errors, so a future refactor that truncates or
            -- filters diagnostic bodies would silently drop the fix-it hint.
            --
            -- Real-world context: a downstream project committed the exact
            -- 3-import shape below during a v0.17.3 upgrade, misled by the
            -- pre-fix diagnostic's generic "Add `as <Alias>`" suggestion
            -- (which is what they already had on line 6). The new hint
            -- surfaces the existing alias line + the reason a bare
            -- `exposing (…)` still trips E1001.
            sky <- findSky
            let libDbSrc = unlines
                    [ "module Lib.Db exposing (conn, dummy)"
                    , ""
                    , "conn : String"
                    , "conn = \"local://\""
                    , ""
                    , "dummy : Int"
                    , "dummy = 0"
                    ]
                mainSrc = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "import Std.Db as Db"
                    , "import Lib.Db as LibDb"
                    , "import Lib.Db exposing (conn)"
                    , ""
                    , "main = let _ = LibDb.dummy in println conn"
                    ]
            withSystemTempDirectory "sky-lsp-dual-import" $ \dir -> do
                let srcDir = dir </> "src"
                createDirectoryIfMissing True (srcDir </> "Lib")
                writeFile (dir </> "sky.toml")
                    "name = \"lsp-dual-import\"\nentry = \"src/Main.sky\"\n"
                writeFile (srcDir </> "Lib" </> "Db.sky") libDbSrc
                let mainPath = srcDir </> "Main.sky"
                writeFile mainPath mainSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin mainPath mainSrc
                    result <- awaitNotification hout
                        "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on 3-import shape"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                                codes = diagnosticCodes payload
                            -- Base E1001 collision surfaces.
                            anyMatch "E1001" codes `shouldBe` True
                            anyMatch "two imports both bind the qualifier" msgs
                                `shouldBe` True
                            -- The improved fix-it recognises the existing
                            -- `import Lib.Db as LibDb` line and points the
                            -- user at the merge shape.
                            anyMatch "You already have" msgs `shouldBe` True
                            anyMatch "import Lib.Db as LibDb exposing" msgs
                                `shouldBe` True
                            -- And explains WHY the bare exposing still
                            -- collides — protects the reasoning from being
                            -- silently truncated in a future refactor.
                            anyMatch "re-registers" msgs `shouldBe` True


    describe "v0.13 Layer 4 — LSP carries stable diagnostic codes" $ do

        it "type-mismatch publishDiagnostics includes code E2001" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "add : Int -> Int -> Int"
                    , "add x y = x + y"
                    , ""
                    , "main = println (add \"hello\" 1)"
                    ]
            withSystemTempDirectory "sky-lsp-code-e2001" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on type-mismatch file"
                        Just payload -> do
                            let codes = diagnosticCodes payload
                            anyMatch "E2001" codes `shouldBe` True

        it "unbound-name publishDiagnostics includes code E1001" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println (frobnicate 42)"
                    ]
            withSystemTempDirectory "sky-lsp-code-e1001" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on unbound-name file"
                        Just payload -> do
                            let codes = diagnosticCodes payload
                            anyMatch "E1001" codes `shouldBe` True

        it "parse-error publishDiagnostics includes code E0001" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "tyep alias Foo = Int"  -- typo: misspelled keyword
                    , ""
                    , "main = 1"
                    ]
            withSystemTempDirectory "sky-lsp-code-e0001" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on parse-error file"
                        Just payload -> do
                            let codes = diagnosticCodes payload
                            anyMatch "E0001" codes `shouldBe` True

        it "every diagnostic includes a non-empty source field" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println bogus"
                    ]
            withSystemTempDirectory "sky-lsp-source-field" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on unbound file"
                        Just payload -> do
                            -- Every diagnostic should have source="sky"
                            -- so VS Code / Cursor / Neovim disambiguate
                            -- Sky diagnostics from other compilers'.
                            let sources = diagnosticSources payload
                            anyMatch "sky" sources `shouldBe` True

        it "Layer 4 — didSave runs the full pipeline (sky check) in the background" $ do
            -- v0.13 Layer 4: on save, the server spawns `sky check`
            -- (codegen + go build, a superset of type-checking) and
            -- publishes its diagnostics.  Here a type error is used
            -- as the trigger because it's deterministic — the full-
            -- pipeline path parses the same `-- TYPE ERROR …` block
            -- the CLI emits and surfaces it with source="sky check".
            -- Codegen-stage-only errors (Issue #52's Model_R class)
            -- flow through the identical path; a type error is the
            -- stable fixture.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "bad : Int -> String"
                    , "bad n = n"
                    , ""
                    , "main = println (bad 5)"
                    ]
            withSystemTempDirectory "sky-lsp-layer4-save" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    -- drain the didOpen diagnostics
                    _ <- awaitNotification hout "textDocument/publishDiagnostics"
                    didSave hin fixture
                    -- The background `sky check` returns and publishes
                    -- its own diagnostics.  Drain notifications until
                    -- we see one carrying source="sky check" (the
                    -- full-pipeline marker) — the synchronous type-
                    -- check pass may emit one first.
                    let drainForFullPipeline :: Int -> IO ()
                        drainForFullPipeline 0 = expectationFailure
                            "no full-pipeline (sky check) diagnostic within budget"
                        drainForFullPipeline n = do
                            r <- awaitNotification hout
                                   "textDocument/publishDiagnostics"
                            case r of
                                Nothing -> expectationFailure
                                    "publishDiagnostics stream ended early"
                                Just payload ->
                                    if anyMatch "sky check"
                                         (diagnosticSources payload)
                                      then do
                                        let msgs = diagnosticMessages payload
                                        anyMatch "type mismatch" msgs
                                            `shouldBe` True
                                      else drainForFullPipeline (n - 1)
                    drainForFullPipeline 5


-- | Extract `source` field strings from publishDiagnostics payload.
diagnosticSources :: Aeson.Value -> [T.Text]
diagnosticSources v = case v of
    Object o -> case KM.lookup "params" o of
        Just (Object p) -> case KM.lookup "diagnostics" p of
            Just (Array arr) -> concatMap getSrc (V.toList arr)
            _ -> []
        _ -> []
    _ -> []
  where
    getSrc (Object d) = case KM.lookup "source" d of
        Just (String t) -> [t]
        _ -> []
    getSrc _ = []
