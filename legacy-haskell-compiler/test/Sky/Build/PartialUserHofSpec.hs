module Sky.Build.PartialUserHofSpec (spec) where

-- Regression spec for #580 — point-free partial application of a
-- Sky-source stdlib HOF (e.g. `Sky.Core.List.map`) into a polymorphic
-- callback slot.
--
-- Trigger: `List.map (List.map dbl) [[1,2],[3,4]]` where `dbl : Int ->
-- Int` (a CONCRETE monomorphic inner function).  The user's matrix
-- (#580) showed identical failures for Task.map / outer List.map /
-- any polymorphic HOF wrapping the inner partial-app.  Both `go
-- build` errors had to be closed:
--
--   1. `cannot use dbl (value of type func(n int) int) as func(any)
--      any value in argument to Sky_Core_List_map_[any, any]`
--   2. `cannot use __pp0 (variable of type any) as []any value in
--      argument to Sky_Core_List_map_[any, any]`
--
-- Root cause was a two-part bug in `emitPartialUserCall`:
--   * The qualified-name lookup key didn't pass the trailing
--     identifier through `goSafeName`, so a callee whose Sky name is
--     a Go predeclared identifier (`map`, `len`, `new`, ...) misses
--     in `_cg_funcParamTypes` (registered key carries `_` suffix).
--     Without param types the wrapper falls back to `func(any) any`.
--   * Even when params were known, the wrapper's inner call carried
--     an explicit `[any, any]` instantiation (from `exprToGo func`)
--     that forced T-vars to any.  A concrete supplied arg (`dbl`)
--     then mismatched the `func(any) any` slot Go computed from the
--     explicit instantiation.
--
-- Post-fix: σ-recovery from supplied args' Go types pins the
-- callee's TVars; missing-slot wrapper params + return carry the
-- substituted shapes (`func(__pp0 []int) any { return
-- Sky_Core_List_map_(dbl, ...) }`); the explicit `[any, any]` is
-- dropped so Go's call-site type inference does the final pinning.
--
-- Crucially, the bad codegen only surfaces when the partial-app's
-- enclosing binding is REFERENCED FROM `main` — Sky's DCE strips
-- unreachable top-level bindings and `go build` never sees the bad
-- shape.  The fixtures here all reference their out-bindings from
-- `main` so the regression remains observable.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


buildInTmp :: FilePath -> String -> (FilePath -> ExitCode -> String -> String -> IO ()) -> IO ()
buildInTmp slug src k = do
    sky <- findSky
    withSystemTempDirectory slug $ \tmp -> do
        writeFile (tmp </> "Main.sky") src
        let cp = (proc sky ["build", "Main.sky"]) { cwd = Just tmp }
        (ec, out, err) <- readCreateProcessWithExitCode cp ""
        k tmp ec out err


runApp :: FilePath -> IO (ExitCode, String, String)
runApp tmp = do
    let bin = tmp </> "sky-out" </> "app"
    let cp = (proc bin []) { cwd = Just tmp }
    readCreateProcessWithExitCode cp ""


spec :: Spec
spec = describe "point-free partial application into polymorphic HOF slot (#580)" $ do

    it "pure outer: List.map (List.map dbl) [[1,2],[3,4]] builds + runs" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "dbl : Int -> Int"
                , "dbl n ="
                , "    n * 2"
                , ""
                , "out : List (List Int)"
                , "out ="
                , "    List.map (List.map dbl) [ [ 1, 2 ], [ 3, 4 ] ]"
                , ""
                , "main ="
                , "    println (String.fromInt (List.length out))"
                ]
        buildInTmp "sky-580-pure" src $ \tmp ec out err -> do
            let combined = out ++ err
            ec `shouldBe` ExitSuccess
            -- Both pre-fix error signatures MUST be absent.
            ("cannot use dbl" `isInfixOf` combined) `shouldBe` False
            ("does not match" `isInfixOf` combined) `shouldBe` False
            (runEc, runOut, _) <- runApp tmp
            runEc `shouldBe` ExitSuccess
            (filter (/= '\n') runOut) `shouldBe` "2"

    it "Task wrap: Task.map (List.map dbl) builds + runs" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Sky.Core.String as String"
                , "import Sky.Core.Task as Task"
                , "import Std.Log exposing (println)"
                , ""
                , "dbl : Int -> Int"
                , "dbl n ="
                , "    n * 2"
                , ""
                , "doubled : Task Error (List Int)"
                , "doubled ="
                , "    Task.map (List.map dbl) (Task.succeed [ 1, 2, 3 ])"
                , ""
                , "main ="
                , "    let _ = doubled in"
                , "    println \"ok\""
                ]
        buildInTmp "sky-580-task" src $ \tmp ec out err -> do
            let combined = out ++ err
            ec `shouldBe` ExitSuccess
            ("cannot use" `isInfixOf` combined) `shouldBe` False
            (runEc, runOut, _) <- runApp tmp
            runEc `shouldBe` ExitSuccess
            (filter (/= '\n') runOut) `shouldBe` "ok"

    -- Non-regression: polymorphic inner (List.head) was already
    -- working pre-fix because there's no concrete-type mismatch to
    -- close. Keep this case green so a future change doesn't break
    -- the no-σ-recovery fallback.
    it "polymorphic inner (Task.map List.head) still builds" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Sky.Core.Task as Task"
                , "import Std.Log exposing (println)"
                , ""
                , "first : Task Error (Maybe Int)"
                , "first ="
                , "    Task.map List.head (Task.succeed [ 1, 2, 3 ])"
                , ""
                , "main ="
                , "    let _ = first in"
                , "    println \"ok\""
                ]
        buildInTmp "sky-580-poly" src $ \_tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess

    -- Non-regression: the eta-expanded form was the documented
    -- workaround pre-fix and must remain green.
    it "eta-expanded form still builds + runs" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "dbl : Int -> Int"
                , "dbl n ="
                , "    n * 2"
                , ""
                , "out : List (List Int)"
                , "out ="
                , "    List.map (\\xs -> List.map dbl xs) [ [ 1, 2 ], [ 3, 4 ] ]"
                , ""
                , "main ="
                , "    println (String.fromInt (List.length out))"
                ]
        buildInTmp "sky-580-eta" src $ \tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess
            (runEc, runOut, _) <- runApp tmp
            runEc `shouldBe` ExitSuccess
            (filter (/= '\n') runOut) `shouldBe` "2"

    -- Codegen shape proof: the wrapper's param carries the
    -- concrete element type ([]int, not []any), and the inner call
    -- is bare (no `[any, any]` instantiation).
    it "emitted Go uses a typed wrapper + bare inner call" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "dbl : Int -> Int"
                , "dbl n ="
                , "    n * 2"
                , ""
                , "out : List (List Int)"
                , "out ="
                , "    List.map (List.map dbl) [ [ 1, 2 ], [ 3, 4 ] ]"
                , ""
                , "main ="
                , "    println (String.fromInt (List.length out))"
                ]
        buildInTmp "sky-580-shape" src $ \tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess
            body <- readFile (tmp </> "sky-out" </> "main.go")
            -- Pre-fix the wrapper was `func(__pp0 any) any { return
            -- Sky_Core_List_map_[any, any](dbl, __pp0); }`. Both
            -- the bare `__pp0 any` and the `[any, any]` MUST be
            -- absent in the post-fix emit.
            ("__pp0 []int" `isInfixOf` body) `shouldBe` True
            ("Sky_Core_List_map_[any, any]" `isInfixOf` body)
                `shouldBe` False
