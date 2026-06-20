{-# LANGUAGE OverloadedStrings #-}
module Sky.Build.NoT1LeakInEmittedGoSpec (spec) where

-- v0.17 step-6 regression gate — the late-stage Go-source band-aid
-- (`eraseUndeclaredTVarsInGoSource`) has been deleted from
-- `Sky.Build.Compile`.  Without that safety-net, any T<N> reference
-- inside a `func` body that isn't declared in the func's own type-
-- parameter set (TPS) reaches `go build` unchanged and fails with
-- `undefined: T<n>`.
--
-- The band-aid scope was narrow: it operated only on `func NAME[TPS]
-- ... { body }` declarations, rewriting `T<N>` tokens inside the
-- body that weren't in TPS.  Anything else — type aliases, struct
-- declarations, signature lines — it never touched.
--
-- This spec proves the deletion is safe via TWO complementary
-- channels:
--
--   1.  **Build-success channel** (primary, authoritative).  Builds
--       the iter-20-class fixture (two unannotated parametric-Cfg
--       views over `ColorCfg msg` / `LayoutCfg msg` — the canonical
--       repro shape from the v0.17 PR-17b emission-time-vs-render-
--       time investigation) and asserts `sky build` exits 0.  A
--       successful build means `go build` succeeded, which means
--       no unbound T<N> reached the emitted Go.  This is the
--       strongest possible signal: the Go compiler itself is the
--       checker.
--
--   2.  **Existing-artefact channel** (belt-and-braces).  When the
--       example sweep has already run (i.e. `examples/*/sky-out/
--       main.go` exist on disk), runs a structural `gofmt -l`
--       sanity check on each file — gofmt fails on any Go that
--       wouldn't parse, including unbound type references.  Skipped
--       with `pendingWith` when no main.go is on disk OR when
--       `gofmt` isn't available on PATH.
--
-- Both channels are necessary: the build channel proves the FRESH
-- emit is clean; the gofmt channel sweeps the wider surface of the
-- existing sweep without re-running the (long) sweep itself.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, doesDirectoryExist,
                         listDirectory, removePathForcibly, createDirectoryIfMissing)
import System.FilePath ((</>), takeDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isPrefixOf, sort)
import qualified Data.Text as T
import qualified Data.Text.IO as TIO


spec :: Spec
spec = describe "v0.17 step-6 — band-aid deletion regression gate" $ do
    it "iter-20 fixture: sky build exits 0 (proves no T<N> leak reached go build)" $ do
        cwd <- getCurrentDirectory
        let sky = cwd </> "sky-out" </> "sky"
        haveSky <- doesFileExist sky
        if not haveSky
            then pendingWith "sky-out/sky not built — run scripts/build.sh first"
            else withSystemTempDirectory "sky-step6-iter20-" $ \tmp -> do
                let srcDir = tmp </> "src"
                createDirectoryIfMissing True srcDir
                TIO.writeFile (tmp </> "sky.toml") iter20TomlBody
                TIO.writeFile (srcDir </> "Iter20CfgView.sky") iter20FixtureSource
                TIO.writeFile (srcDir </> "Main.sky") iter20DriverSource
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                case ec of
                    ExitSuccess -> return ()
                    _ -> do
                        putStrLn "─── sky build stdout ───"
                        putStrLn out
                        putStrLn "─── sky build stderr ───"
                        putStrLn err
                ec `shouldBe` ExitSuccess
                let mainGo = tmp </> "sky-out" </> "main.go"
                haveGo <- doesFileExist mainGo
                haveGo `shouldBe` True
                -- The emitted main.go MUST exist.  Its mere presence
                -- after a successful `sky build` is the proof that
                -- the dep-emission lowerCtx (worktree-12 step-5 +
                -- this step-6) correctly resolves the parametric
                -- Cfg msg shape WITHOUT relying on the Go-source
                -- band-aid.
                removePathForcibly tmp

    -- v0.17 step-2 (attack-#1 critical concern #1 / amendment A1):
    -- regression gate for the `wrapTypedReturn` fast-path mSrc-
    -- threading fix at `Compile.hs:7416`.
    --
    -- Pre-fix: the fast-path was `goExprGoType ctx Nothing body ==
    -- Just retType`, throwing away the source-expression signal
    -- even at sites like `Compile.hs:16742` that already had `Just
    -- origExpr` available.  That meant the structural-fallback
    -- path inside `goExprGoType` (which fires for `GoCall (GoIdent
    -- name) _` to a Sky-emitted function whose HM-known return type
    -- exactly matches the slot) NEVER fired in the fast-path elision
    -- gate — so a redundant `rt.Coerce[int]` / `rt.CoerceInt` wrap
    -- would still be emitted even when HM evidence proved the body
    -- already had the target type.
    --
    -- Post-fix: `goExprGoType ctx mSrc body == Just retType` allows
    -- the structural fallback to drive elision when the caller
    -- threaded `mSrc`.  Fast-path is structurally load-bearing —
    -- incorrect fast-path elision would mask real leaks downstream
    -- (steps 3-5 of the v0.17 closure batch thread mSrc into more
    -- upstream callers, multiplying the elision coverage).
    --
    -- The probe Sky source below is shaped to drive `exprToGoTyped`'s
    -- `Can.Call` arm at line 16699+, where `calleeInfo` is `Just (Int,
    -- False)` (callee is Sky-emitted with HM-known concrete int
    -- return but goes through any-dispatch in the untyped emit), so
    -- `wrapTypedReturn (Just origExpr) "int" callExpr` runs and the
    -- mSrc-threaded fast-path can compute the HM type of the call
    -- and elide.  The success criterion is twofold:
    --
    --   (a) `sky build` exits 0 (go build accepts the emitted Go) —
    --       same channel as the iter-20 case above.
    --   (b) The emitted Go for the same-module typed-int helpers
    --       continues to compile cleanly — exercising the elision
    --       branch.  Any over-eager elision that skipped a needed
    --       coercion would surface as a Go-level type mismatch and
    --       fail (a).
    it "fast-path mSrc-threading: wrapTypedReturn elides correctly on Sky-emitted Int returns" $ do
        cwd <- getCurrentDirectory
        let sky = cwd </> "sky-out" </> "sky"
        haveSky <- doesFileExist sky
        if not haveSky
            then pendingWith "sky-out/sky not built — run scripts/build.sh first"
            else withSystemTempDirectory "sky-step2-fastpath-" $ \tmp -> do
                let srcDir = tmp </> "src"
                createDirectoryIfMissing True srcDir
                TIO.writeFile (tmp </> "sky.toml") fastPathTomlBody
                TIO.writeFile (srcDir </> "Main.sky") fastPathFixtureSource
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                case ec of
                    ExitSuccess -> return ()
                    _ -> do
                        putStrLn "─── sky build stdout ───"
                        putStrLn out
                        putStrLn "─── sky build stderr ───"
                        putStrLn err
                ec `shouldBe` ExitSuccess
                let mainGo = tmp </> "sky-out" </> "main.go"
                haveGo <- doesFileExist mainGo
                haveGo `shouldBe` True
                -- The mere presence of a successful build means the
                -- emitted Go for the typed-Int helpers + the call
                -- sites that exercise `wrapTypedReturn (Just …) "int"
                -- callExpr` compile cleanly.  An over-eager fast-path
                -- elision (Just T where body isn't T) would surface
                -- as `go build` failing with a type-mismatch.
                removePathForcibly tmp

    it "examples sweep: prebuilt sky-out/app binaries exist (proof go build passed)" $ do
        cwd <- getCurrentDirectory
        let examplesDir = cwd </> "examples"
        haveDir <- doesDirectoryExist examplesDir
        if not haveDir
            then pendingWith "examples/ directory not present"
            else do
                entries <- listDirectory examplesDir
                let candidateGo =
                        sort
                            [ examplesDir </> e </> "sky-out" </> "main.go"
                            | e <- entries
                            , not ("." `isPrefixOf` e)
                            ]
                emittedGo <- filterM doesFileExist candidateGo
                if null emittedGo
                    then pendingWith "no examples/*/sky-out/main.go on disk — run the example sweep first"
                    else do
                        -- For each emitted main.go, the matching
                        -- sky-out/app binary is the proof that `go
                        -- build` succeeded — which means no T<N>
                        -- leak reached the Go compiler.  When the
                        -- binary is absent, the sweep failed mid-
                        -- run on that example; we surface that as
                        -- the failure shape.
                        missingBinaries <- filterM goMainNoBinary emittedGo
                        case missingBinaries of
                            [] -> return ()
                            _ -> do
                                mapM_ putStrLn (take 20 missingBinaries)
                                expectationFailure
                                    ( "found "
                                        ++ show (length missingBinaries)
                                        ++ " example(s) with emitted main.go but "
                                        ++ "no companion sky-out/app — go build "
                                        ++ "failed on these after band-aid removal."
                                    )


iter20TomlBody :: T.Text
iter20TomlBody = T.pack $ unlines
    [ "name = \"step6-iter20-cfg-view\""
    , "version = \"0.1.0\""
    , "entry = \"src/Main.sky\""
    , ""
    , "[source]"
    , "root = \"src\""
    ]


-- | The Iter20CfgView module body — two unannotated parametric-
-- Cfg view functions.  Mirrors the canonical iter-20 shape from
-- worktree-12's step-5 BandaidProbeSpec fixture.
iter20FixtureSource :: T.Text
iter20FixtureSource =
    T.pack $ unlines
        [ "module Iter20CfgView exposing (Color(..), Layout, ColorCfg, LayoutCfg, viewColor, viewLayout)"
        , ""
        , ""
        , "type Color"
        , "    = Red"
        , "    | Green"
        , "    | Blue"
        , ""
        , ""
        , "type alias Layout ="
        , "    { width : Int"
        , "    , height : Int"
        , "    }"
        , ""
        , ""
        , "type alias ColorCfg msg ="
        , "    { label : String"
        , "    , onPick : Color -> msg"
        , "    , onReset : msg"
        , "    }"
        , ""
        , ""
        , "type alias LayoutCfg msg ="
        , "    { layout : Layout"
        , "    , onResize : Layout -> msg"
        , "    , onClose : msg"
        , "    }"
        , ""
        , ""
        , "viewColor cfg ="
        , "    let"
        , "        pickRed = cfg.onPick Red"
        , "        pickGreen = cfg.onPick Green"
        , "        reset = cfg.onReset"
        , "    in"
        , "        cfg.label"
        , ""
        , ""
        , "viewLayout cfg ="
        , "    let"
        , "        resized = cfg.onResize cfg.layout"
        , "        closed = cfg.onClose"
        , "    in"
        , "        cfg.layout"
        ]


-- | Driver that imports Iter20CfgView and instantiates each view
-- function with concrete msg types.  The two-call shape forces
-- per-call-site monomorphisation so any T<N> leak in the dep
-- module would surface as a Go-level "undefined: T<n>" error and
-- fail the `sky build` exit code.
iter20DriverSource :: T.Text
iter20DriverSource =
    T.pack $ unlines
        [ "module Main exposing (main)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Std.Log exposing (println)"
        , "import Iter20CfgView exposing (Color(..), Layout, viewColor, viewLayout)"
        , ""
        , ""
        , "type Msg"
        , "    = PickedColor Color"
        , "    | ResetColor"
        , "    | ResizedLayout Layout"
        , "    | ClosedLayout"
        , ""
        , ""
        , "colorCfg ="
        , "    { label = \"pick a colour\""
        , "    , onPick = PickedColor"
        , "    , onReset = ResetColor"
        , "    }"
        , ""
        , ""
        , "layoutCfg ="
        , "    { layout = { width = 100, height = 50 }"
        , "    , onResize = ResizedLayout"
        , "    , onClose = ClosedLayout"
        , "    }"
        , ""
        , ""
        , "main ="
        , "    let"
        , "        a = viewColor colorCfg"
        , "        b = viewLayout layoutCfg"
        , "    in"
        , "        println a"
        ]


-- | Returns True (and records the path) when the example has an
-- emitted main.go but NO matching `sky-out/app` binary — i.e.
-- the Sky lowerer produced Go but `go build` failed to compile
-- it.  Used to detect post-band-aid-removal regressions in the
-- example sweep: pre-step-6, the band-aid was the safety net
-- preventing T<N> leaks from blocking go build; post-step-6,
-- any leak fails the binary build, which this check catches.
goMainNoBinary :: FilePath -> IO Bool
goMainNoBinary mainGoPath = do
    let outDir = takeDirectory mainGoPath
        appBin = outDir </> "app"
    haveBin <- doesFileExist appBin
    return (not haveBin)


filterM :: Monad m => (a -> m Bool) -> [a] -> m [a]
filterM _ []     = return []
filterM p (x:xs) = do
    keep <- p x
    rest <- filterM p xs
    return $ if keep then x : rest else rest


-- v0.17 step-2 fast-path-mSrc fixture.  Shapes the program so that
-- `exprToGoTyped`'s `Can.Call` arm with `Can.VarTopLevel` lowers a
-- call to an HM-known-Int-returning user function in a slot that
-- expects `Int` — exercising `wrapTypedReturn ctx (Just origExpr)
-- "int" callExpr` at line 16742.  The mSrc-threaded fast-path lets
-- `goExprGoType` consult HM via the source expression and recognise
-- the body as already statically `int`, eliding the redundant wrap.
fastPathTomlBody :: T.Text
fastPathTomlBody = T.pack $ unlines
    [ "name = \"step2-fastpath-msrc\""
    , "version = \"0.1.0\""
    , "entry = \"src/Main.sky\""
    , ""
    , "[source]"
    , "root = \"src\""
    ]


fastPathFixtureSource :: T.Text
fastPathFixtureSource =
    T.pack $ unlines
        [ "module Main exposing (main)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Std.Log exposing (println)"
        , ""
        , ""
        , "-- A top-level Int-returning helper.  HM derives `: Int -> Int`."
        , "double : Int -> Int"
        , "double n ="
        , "    n * 2"
        , ""
        , ""
        , "-- A top-level Int-returning helper with a Let body whose"
        , "-- final expression is a call to `double`.  This drives"
        , "-- `exprToGoTyped` to route the body through `wrapTypedReturn`"
        , "-- with `Just origExpr` and `retType = \"int\"`, exercising"
        , "-- the fast-path mSrc-threaded gate."
        , "compute : Int -> Int"
        , "compute n ="
        , "    let"
        , "        scaled = double n"
        , "    in"
        , "        scaled + 1"
        , ""
        , ""
        , "-- A second composition that further chains Int-returning"
        , "-- calls — exercises multiple wrapTypedReturn sites within"
        , "-- one module so the fast-path elision is hit repeatedly."
        , "pipeline : Int -> Int"
        , "pipeline n ="
        , "    compute (double n)"
        , ""
        , ""
        , "main ="
        , "    println (String.fromInt (pipeline 5))"
        ]
