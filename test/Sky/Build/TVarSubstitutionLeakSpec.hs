module Sky.Build.TVarSubstitutionLeakSpec (spec) where

-- v0.17.2 T-var substitution-leak regression.
--
-- Trigger shape (minimal):
--
--   type alias Cfg msg =
--       { label : String, onCheck : msg, onDismiss : msg }
--
--   view : Cfg msg -> String
--   view cfg =
--       let onCheck   = cfg.onCheck
--           onDismiss = cfg.onDismiss
--       in toolbar cfg onCheck ++ diagnostics cfg onDismiss
--
--   toolbar     : Cfg msg -> msg -> String
--   diagnostics : Cfg msg -> msg -> String
--
-- Pre-fix, the polymorphic caller `view[T1 any]` lowered its
-- sibling-helper calls through the fallback arm of
-- `coerceCallArgsAt` in `src/Sky/Build/Compile.hs`.  That arm
-- α-renames the callee's declared TVars into the callee-private
-- 9000-space (`T1` → `T9001`) via `alphaRenameCalleeTVars`, so the
-- caller's own T-var scope check fires "out of scope" and the
-- erase-to-`any` fallback triggers.
--
-- The `identityRecovered` HM branch — added in v0.16.13 (#530) for
-- parametric-record-alias args — self-mapped every α-renamed tvar
-- to itself when the source's HM alias base matched the param's
-- alias base.  That self-mapping populated `recovered = {T9001 →
-- T9001}` which then bypassed `substituteOnly`'s unbound-tvar +
-- erase-scoped fallback, and the fake `T9001` name leaked into
-- emitted Go verbatim as `rt.Coerce[T9001](onCheck)`.  `go build`
-- rejected the file with `undefined: T9001`.
--
-- The fix (`src/Sky/Build/Compile.hs` — `identityRecovered` at the
-- fallback arm's σ-recovery block) gates the identity mapping on
-- `enclosingTypeParamInScopeCtx ctx tv`: identity is only sound
-- when the tvar is actually in the caller's enclosing scope
-- (Monomorphise's `substTypeParamsInString` needs a live tvar to
-- rewrite).  α-renamed tvars (never in caller scope) fall through
-- to `substituteOnly`'s `outOfScope` → `eraseScopedCtx` path,
-- which widens to `any` so Go's call-site inference pins the
-- callee's generic consistently across sibling args.
--
-- This spec:
--   1. Builds the minimal fixture and asserts `sky build` returns
--      `Compilation successful` (Sky lowering + `go build` clean).
--   2. Greps the emitted Go for any `T<n>` where n >= 1000 —
--      the α-rename token space.  ZERO occurrences is the
--      soundness invariant (per `alphaRenameCalleeTVars`'s
--      contract: "no T9NNN token may leak into emitted Go").
--   3. Runs the built binary and asserts the expected output
--      (`[toolbar:hi][diag:hi]`) — end-to-end runtime safety.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)
import qualified Data.Char as Char


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run cabal install --installdir=./sky-out first")


runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args workDir = do
    let cp = (proc sky args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""


-- Minimal fixture that reproduces the α-rename identity-leak class:
-- a polymorphic view over a parametric-record-alias cfg calls two
-- sibling helpers, each taking the cfg AND a bare-msg arg
-- let-bound off cfg's fields.  The let-binding makes goExprGoType
-- return Nothing for the bare-msg arg, which is what steers the
-- code into the fallback arm's σ-recovery (structuralRecovered
-- can't fire; identityRecovered was self-pinning the α-renamed
-- name).
skyToml :: String
skyToml = unlines
    [ "name = \"tvarleak\""
    , "version = \"0.1.0\""
    , "entry = \"src/Main.sky\""
    ]


mainSky :: String
mainSky = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "type alias Cfg msg ="
    , "    { label : String"
    , "    , onCheck : msg"
    , "    , onDismiss : msg"
    , "    }"
    , ""
    , ""
    , "view : Cfg msg -> String"
    , "view cfg ="
    , "    let"
    , "        onCheck = cfg.onCheck"
    , "        onDismiss = cfg.onDismiss"
    , "    in"
    , "        toolbar cfg onCheck ++ diagnostics cfg onDismiss"
    , ""
    , ""
    , "toolbar : Cfg msg -> msg -> String"
    , "toolbar cfg _ ="
    , "    \"[toolbar:\" ++ cfg.label ++ \"]\""
    , ""
    , ""
    , "diagnostics : Cfg msg -> msg -> String"
    , "diagnostics cfg _ ="
    , "    \"[diag:\" ++ cfg.label ++ \"]\""
    , ""
    , ""
    , "type Msg"
    , "    = Check"
    , "    | Dismiss"
    , ""
    , ""
    , "main ="
    , "    println (view { label = \"hi\", onCheck = Check, onDismiss = Dismiss })"
    ]


-- Match tokens of the shape `T<digits>` where digits form a number
-- >= 1000.  The α-rename bump is 9000 (see `alphaRenameCalleeTVarsBump`
-- in Compile.hs); user-visible tvars from the Sky compiler are
-- always single-digit or low-double-digit.  A 4+-digit T token in
-- the emit is by construction an α-rename leak.
containsAlphaRenamedTVarLeak :: String -> Bool
containsAlphaRenamedTVarLeak = go Nothing
  where
    go _ [] = False
    go prev ('T':rest)
        | not (maybe False isIdChar prev)
        , (digits, after) <- span Char.isDigit rest
        , length digits >= 4
        , null after || not (isIdChar (head after))
        = True
    go _ (c:cs) = go (Just c) cs
    isIdChar c = Char.isAlphaNum c || c == '_' || c == '.'


spec :: Spec
spec = describe "v0.17.2 — α-rename T-var substitution leak" $ do

    it "the minimal fixture builds clean end-to-end" $
      withSystemTempDirectory "sky-tvarleak-build" $ \dir -> do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml") skyToml
        writeFile (dir </> "src" </> "Main.sky") mainSky
        sky <- findSky
        (exit, stdout', stderr') <-
            runSky sky ["build", "src/Main.sky"] dir
        let combined = stdout' ++ "\n" ++ stderr'
        exit `shouldBe` ExitSuccess
        ("Compilation successful" `isInfixOf` combined) `shouldBe` True

    it "the emitted Go contains no α-renamed T<n>-in-9000-space token" $
      withSystemTempDirectory "sky-tvarleak-emit" $ \dir -> do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml") skyToml
        writeFile (dir </> "src" </> "Main.sky") mainSky
        sky <- findSky
        (exit, _stdout, _stderr) <-
            runSky sky ["build", "src/Main.sky"] dir
        exit `shouldBe` ExitSuccess
        emitted <- readFile (dir </> "sky-out" </> "main.go")
        -- Invariant: `alphaRenameCalleeTVars` bumps every callee tvar
        -- by 9000 as a caller-private namespace.  Any 4-digit T
        -- surviving into emitted Go is a leak.
        containsAlphaRenamedTVarLeak emitted `shouldBe` False

    it "the built binary runs and prints the concatenated sibling-helper output" $
      withSystemTempDirectory "sky-tvarleak-run" $ \dir -> do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml") skyToml
        writeFile (dir </> "src" </> "Main.sky") mainSky
        sky <- findSky
        (exit, _stdout, _stderr) <-
            runSky sky ["build", "src/Main.sky"] dir
        exit `shouldBe` ExitSuccess
        let cp = (proc (dir </> "sky-out" </> "app") []) { cwd = Just dir }
        (runExit, runStdout, _runStderr) <-
            readCreateProcessWithExitCode cp ""
        runExit `shouldBe` ExitSuccess
        -- Trim trailing newlines from println output before compare.
        let trimmed = reverse (dropWhile (== '\n') (reverse runStdout))
        trimmed `shouldBe` "[toolbar:hi][diag:hi]"
