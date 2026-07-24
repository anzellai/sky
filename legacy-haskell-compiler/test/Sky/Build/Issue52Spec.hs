-- | Regression tests for issue #52
-- (https://github.com/anzellai/sky/issues/52).
--
-- A contributor surfaced three distinct holes in the v0.11 → v0.12
-- compiler that all manifested as cryptic Go-build errors instead of
-- Sky-shaped diagnostics:
--
--   1. `List.drop` with an any-typed Int arg (record field, AdtField,
--      function-call result) emitted `rt.List_dropT[int](anyArg, ...)`
--      and `go build` rejected the missing type assertion.
--
--   2. `{ model | field = X }` accepted any X-typed value, even when
--      `field` was declared with a different type. HM emitted no
--      constraint; the runtime panicked with
--      `interface conversion: interface {} is int, not string`.
--
--   3. (Lives in Sky.Canonicalise.UnboundSpec) Names inside
--      `Src.Paren` (parenthesised expressions like `(loadExample i)`)
--      were silently dropped by the unbound-name walker because
--      `Src.Paren` fell through `_ -> []` in
--      `collectUnqualExprRegions`.
--
-- This file covers (1) and (2). The Paren case is in UnboundSpec.
module Sky.Build.Issue52Spec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         copyFile, doesFileExist, listDirectory, doesDirectoryExist)
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


copyTree :: FilePath -> FilePath -> IO ()
copyTree src dst = do
    createDirectoryIfMissing True dst
    entries <- listDirectory src
    mapM_ (\e -> do
        let s = src </> e
            d = dst </> e
        isF <- doesFileExist s
        if isF
            then copyFile s d
            else do
                isD <- doesDirectoryExist s
                if isD then copyTree s d else return ()) entries


spec :: Spec
spec = do
    describe "Issue #52: List.drop typed-Int arg coercion" $ do
        it "accepts a record-field-derived Int arg without a Go build error" $ do
            -- Pre-fix, `List.drop m.i [1, 2, 3]` lowered to
            -- `rt.List_dropT[int](rt.Field(m, "I"), ...)` where the
            -- first arg is any-typed. Go rejected the typed-generic
            -- dispatch ("cannot use ... (any) as int value in
            -- argument to rt.List_dropT[int]: need type assertion").
            -- Post-fix, the kernel routing wraps via `rt.AsInt(...)`.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "issue52-listdrop"
            withSystemTempDirectory "sky-issue52-listdrop" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                let combined = out ++ err
                ec `shouldBe` ExitSuccess
                -- Must not see the Go build error.
                combined `shouldSatisfy` \s ->
                    not ("need type assertion" `isInfixOf` s)
                combined `shouldSatisfy` \s ->
                    not ("rt.List_dropT[int]" `isInfixOf` s && "any" `isInfixOf` s)

    describe "Issue #52: record update field-type mismatch" $ do
        -- DEFERRED to v0.13. v0.12.1 first attempted an HM-level fix
        -- (row-poly partial-record constraint on `Can.Update`) but
        -- the open-row shape conflicts with closed-alias annotations
        -- (skyvote regression: `update : Msg -> Model -> ...` rejects
        -- the accumulated open-row baseTv against the closed Model
        -- alias). Proper fix needs the v0.13 Diagnostic-AST
        -- refactor — codegen-stage validation will catch this at
        -- the typed Go output layer without overloading HM with
        -- row-poly constraints that conflict with closed aliases.
        it "[v0.13 DEFERRED] rejects `{ m | field = wrongType }` at HM time" $ do
            pendingWith "deferred to v0.13 — needs Diagnostic-AST + codegen-stage validation layer"
