module Sky.Build.CpsStackConstantBound.Shared
    ( -- * Static-analysis combinators (operate on emitted Go text)
      assertHelperEmitted
    , assertNoKernelFallback
    , assertForContinueInHelper
      -- * Runtime constant-stack combinator (compiles + runs a fixture)
    , assertConstantStack1M
      -- * Per-op fixture builder
    , buildOpFixture
    ) where

-- | Shared test helper for the v0.17 step-8 CPS rewrite family
-- (Limitation #8 close). The umbrella goal: rewrite every non-TCO
-- stdlib list / Maybe / Result operation so the emitted Go runs in
-- CONSTANT stack instead of O(N) recursion. The 13 ops in scope:
-- map / filter / foldr / length / concat / take / append / range /
-- zip / concatMap / indexedMap / Maybe.combine / Result.combine.
--
-- Each op gets its own @<Op>Spec.hs@ under
-- @test/Sky/Build/CpsStackConstantBound/@. Every spec follows the
-- same shape — invoke the combinators here against a small
-- fixture — so parallel agents adding a new op only touch their
-- own Spec module + the Sky-source rewrite. The combinators below
-- cover both the static-codegen contract (helper emitted, no
-- kernel fallback, for-continue inside the helper) AND the runtime
-- contract (the op survives a 1M-element input without stack
-- overflow).
--
-- Why these specific combinators?
--
--   1. 'assertHelperEmitted' — the CPS rewrite shape is
--      @op fn list = opHelp fn list initialAcc@. The helper is the
--      tail-recursive worker; the public binding is a thin shim
--      that calls it. Missing helper means the rewrite didn't
--      land (or the typed-lowerer rejected it).
--
--   2. 'assertNoKernelFallback' — when typed-lowerer fails to
--      monomorphise a HOF, codegen falls back to @rt.List_map(...)@
--      (an @any@-typed kernel that lives in
--      @runtime-go/rt/rt.go@). The fallback path is slower (~10x)
--      AND defeats the CPS rewrite entirely because the Sky-source
--      helper isn't called. Any kernel-fallback occurrence at a
--      user-code call site indicates the typed-lowerer regressed.
--
--   3. 'assertConstantStack1M' — the load-bearing runtime gate.
--      Go's default goroutine stack starts at 8 KiB and grows
--      adaptively, but the runtime cost of unbounded growth
--      eventually trips a stack-overflow panic
--      (@runtime: goroutine stack exceeds 1000000000-byte limit@).
--      A 1M-element non-TCO recursion needs ~24 MiB of stack PER
--      goroutine — well past the practical ceiling. If the
--      rewrite is real (auto-TCO @for { ... continue }@) the
--      fixture completes in <2s. If it isn't, the process dies
--      with a stack-overflow panic AND a non-zero exit code.
--
--   4. 'assertForContinueInHelper' — for delegating bindings
--      (e.g. @foldr fn z list = foldrHelp fn z list []@) the
--      tail-recursion guard MUST be on the HELPER's Go function,
--      not the public binding. The public binding is just a shim
--      that calls into the helper once. Greps the helper's Go
--      block specifically.

import Control.Exception (catch, SomeException)
import Data.List (isInfixOf, isPrefixOf, tails)
import System.Directory (createDirectoryIfMissing, doesFileExist,
                         getCurrentDirectory)
import System.Exit (ExitCode(..))
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc,
                      CreateProcess(..))
import Test.Hspec (Expectation, expectationFailure, shouldBe,
                   shouldSatisfy)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..),
                                           compileInProcess)


-- ─── Static-analysis combinators ──────────────────────────────────


-- | Assert that the emitted Go contains a tail-recursive helper
-- of the form @func Sky_Core_<Mod>_<opName>Help@.
--
-- @assertHelperEmitted "List" "map" mainGoText@ greps for
-- @func Sky_Core_List_mapHelp@ in @mainGoText@ and fails the spec
-- if absent — the CPS rewrite landed only when the helper exists.
-- Polymorphic helpers carry a type-parameter list — we anchor on
-- the prefix @func Sky_Core_<Mod>_<opName>Help@ and accept any
-- @[T1 any, T2 any]@ suffix.
assertHelperEmitted :: String  -- ^ module name (e.g. @"List"@, @"Maybe"@)
                    -> String  -- ^ op name (e.g. @"map"@, @"foldr"@)
                    -> String  -- ^ emitted main.go contents
                    -> Expectation
assertHelperEmitted modName opName mainGoText =
    let needle = "func Sky_Core_" ++ modName ++ "_" ++ opName ++ "Help"
        hits   = filter (needle `isInfixOf`) (lines mainGoText)
    in case hits of
        [] -> expectationFailure
                ("CPS rewrite missing: no `" ++ needle ++
                 "` declaration found in emitted main.go. "
                 ++ "The public `" ++ opName ++ "` binding should "
                 ++ "be a shim that calls `" ++ opName ++ "Help …`.")
        _  -> return ()


-- | Assert that the emitted Go contains ZERO occurrences of the
-- kernel-fallback @rt.List_<op>@ path at user-code call sites.
--
-- The fallback path is the @any@-typed runtime kernel
-- (@func List_map(fn any, list any) any@ in
-- @runtime-go/rt/rt.go@). When the typed-lowerer succeeds, the
-- call site emits the Sky-source @Sky_Core_List_op@ instead — the
-- generic @[T1 any, T2 any]@ shape. Any @rt.List_op(@ occurrence
-- in emitted Go indicates the typed-lowerer fell back, which
-- defeats the CPS rewrite (the kernel runs non-TCO Go reflection
-- inside @runtime-go/rt/rt.go@).
--
-- Module names are typed strictly — pass @"List"@ for list ops,
-- not @"Sky_Core_List"@; the helper prepends the @rt.@ qualifier.
assertNoKernelFallback :: String  -- ^ module name (e.g. @"List"@)
                       -> String  -- ^ op name (e.g. @"map"@)
                       -> String  -- ^ emitted main.go contents
                       -> Expectation
assertNoKernelFallback modName opName mainGoText =
    let -- Two shapes we forbid:
        --   1. `rt.List_mapAny(` — explicit any-suffix variant
        --      (not currently emitted; future-proof gate).
        --   2. `rt.List_map(` — the @any@-typed kernel itself.
        --
        -- We deliberately accept `rt.AsListT[T]` and `rt.AsList`
        -- — those are typed coercion helpers, NOT the kernel
        -- fallback.
        needle1 = "rt." ++ modName ++ "_" ++ opName ++ "Any("
        needle2 = "rt." ++ modName ++ "_" ++ opName ++ "("
        hits1   = countOccurrences needle1 mainGoText
        hits2   = countOccurrences needle2 mainGoText
        total   = hits1 + hits2
    in if total > 0
        then expectationFailure
            ("Kernel fallback detected: "
             ++ show total ++ " occurrence(s) of "
             ++ "`" ++ needle1 ++ "` or `" ++ needle2 ++ "` "
             ++ "in emitted main.go. The typed-lowerer should "
             ++ "monomorphise to `Sky_Core_" ++ modName ++ "_"
             ++ opName ++ "[T1 …]` — kernel fallback defeats the "
             ++ "CPS rewrite.")
        else return ()


-- | Assert that the @for { ... continue @-shaped auto-TCO loop is
-- emitted INSIDE the helper function's Go block, not the public
-- binding's. Used for delegating bindings — e.g. @foldr fn z
-- list = foldrHelp fn z list []@ where the tail-recursion is on
-- @foldrHelp@, not @foldr@.
--
-- @assertForContinueInHelper "Sky_Core_List_foldrHelp" mainGoText@
-- locates the named helper's declaration and inspects ONLY the
-- bytes between that declaration's opening @{@ and the matching
-- closing @}@. Looks for both @for {@ (or @for ;;@ etc.) AND a
-- @continue@ statement.
--
-- This is stricter than `isInfixOf "continue" mainGoText` because
-- a sibling tail-recursive function in the same module would
-- match a global grep but doesn't prove THIS helper is TCO'd.
assertForContinueInHelper :: String  -- ^ fully-qualified helper name
                                     --   (e.g. @"Sky_Core_List_mapHelp"@)
                          -> String  -- ^ emitted main.go contents
                          -> Expectation
assertForContinueInHelper helperName mainGoText =
    let needle      = "func " ++ helperName
        body        = extractFuncBody needle mainGoText
        hasFor      = "for {" `isInfixOf` body
                   || "for ;" `isInfixOf` body
                   || "for(" `isInfixOf` body
        hasContinue = "continue" `isInfixOf` body
    in if null body
        then expectationFailure
            ("assertForContinueInHelper: helper `" ++ helperName
             ++ "` not found in emitted main.go — cannot verify "
             ++ "tail-recursion gate.")
        else if not hasFor
            then expectationFailure
                ("Helper `" ++ helperName ++ "` is NOT auto-TCO'd: "
                 ++ "no `for { ... }` loop in its Go body. "
                 ++ "Sky.Build.TailCallOpt failed to detect tail "
                 ++ "recursion — fixture is O(N) stack.")
            else if not hasContinue
                then expectationFailure
                    ("Helper `" ++ helperName ++ "` has `for { ... }` "
                     ++ "but no `continue` statement — the loop is "
                     ++ "structural-only, not a tail-call rewrite.")
                else return ()


-- | Extract a Go function's body (text between the first @{@ on
-- the declaration line and the next top-level @}@). Brace-counted
-- so nested @{ ... }@ blocks are preserved.
extractFuncBody :: String -> String -> String
extractFuncBody needle src =
    case dropWhile (not . (needle `isInfixOf`)) (lines src) of
        []     -> ""
        (h:rest)  ->
            -- Start after the opening line's `{` (declaration may
            -- run across one line: `func foo() { ... }` or two).
            -- Concatenate from the opening brace forward and
            -- brace-count until we close back to depth 0.
            let after  = dropWhile (/= '{') h
                stream = case after of
                    ('{':xs) -> xs ++ "\n" ++ unlines rest
                    _        -> unlines rest  -- brace on next line
            in takeUntilDepthZero stream

  where
    takeUntilDepthZero :: String -> String
    takeUntilDepthZero = go 1 ""
      where
        go :: Int -> String -> String -> String
        go _ acc [] = reverse acc
        go 0 acc _  = reverse acc
        go d acc (c:cs)
          | c == '{' = go (d + 1) (c:acc) cs
          | c == '}' = let d' = d - 1
                       in if d' == 0
                          then reverse acc
                          else go d' (c:acc) cs
          | otherwise = go d (c:acc) cs


-- | Count non-overlapping occurrences of a substring in a string.
countOccurrences :: String -> String -> Int
countOccurrences needle haystack =
    length . filter (needle `isPrefixOf`) . tails $ haystack


-- ─── Runtime constant-stack combinator ────────────────────────────


-- | Compile a multi-file Sky fixture and run its @tests/*.sky@
-- under @sky test@. Asserts the process exits 0 (no stack
-- overflow, no panic, no test failure) and stdout / stderr
-- contains @expectedSubstring@ (typically @"4 passed, 0 failed"@
-- or similar Sky.Test output marker).
--
-- This is the load-bearing runtime gate for the CPS rewrite. A
-- non-TCO recursion at 1M depth blows past Go's @maxstacksize@
-- (default 1 GiB) and panics with
-- @runtime: goroutine stack exceeds 1000000000-byte limit@. The
-- helper observes exit code 0 ↔ rewrite succeeded.
--
-- IMPORTANT: this combinator uses a subprocess @sky test@ — it is
-- the only path that actually executes the emitted Go. The
-- static-analysis combinators above use the in-process compile
-- helper (no subprocess). Pay this cost only when verifying the
-- runtime contract.
--
-- The 'fixtureName' argument is the bare file name (e.g.
-- @"MapStackTest.sky"@); the helper places it under @tests/@ and
-- a stub @src/Main.sky@ alongside it.
assertConstantStack1M
    :: String     -- ^ op name (used in error messages)
    -> [(FilePath, String)]
                  -- ^ project files — pairs of (relative path,
                  --   contents). MUST include a @"src/Main.sky"@
                  --   entry and at least one @"tests/*.sky"@ test
                  --   file. The helper invokes @sky test tests/X@
                  --   for the first file under @tests/@.
    -> String     -- ^ expected substring in @sky test@ stdout
                  --   (typically @"passed, 0 failed"@). Use this
                  --   to gate on Sky.Test's pass-summary line.
    -> Expectation
assertConstantStack1M opName files expectedSubstring = do
    sky <- findSkyBin
    let testFile = case filter (("tests" `isPrefixOf`) . fst) files of
            ((p, _):_) -> p
            []         -> error
                ("assertConstantStack1M (" ++ opName
                 ++ "): no tests/*.sky file in fixture — "
                 ++ "the helper needs a Sky.Test entry to drive.")
    withSystemTempDirectory ("cps-" ++ opName) $ \tmp -> do
        -- Materialise sky.toml + every fixture file.
        writeFile (tmp </> "sky.toml")
            (unlines
                [ "name = \"cps-" ++ opName ++ "-fixture\""
                , "version = \"0.0.0\""
                , "entry = \"src/Main.sky\""
                , ""
                , "[source]"
                , "root = \"src\""
                ])
        mapM_ (\(rel, content) -> do
                let full = tmp </> rel
                createDirectoryIfMissing True (takeDir full)
                writeFile full content)
            files
        -- Bounded subprocess — 120 s ceiling. A stack-overflow
        -- panic in a non-TCO recursive op typically trips within
        -- a few seconds; the bound stops a wedge from hanging the
        -- whole spec suite.
        (ec, stdOut, stdErr) <- runSkyTest sky testFile tmp
            `catch` (\e -> return
                (ExitFailure 1, "", show (e :: SomeException)))
        case ec of
            ExitSuccess ->
                (stdOut ++ stdErr) `shouldSatisfy`
                    (expectedSubstring `isInfixOf`)
            ExitFailure code ->
                expectationFailure
                    ("`sky test " ++ testFile ++ "` failed for op `"
                     ++ opName ++ "` with exit code " ++ show code
                     ++ ":\n--- stdout ---\n" ++ stdOut
                     ++ "\n--- stderr ---\n" ++ stdErr
                     ++ "\n(A stack-overflow panic here means the "
                     ++ "CPS rewrite is missing or the typed-lowerer "
                     ++ "regressed — non-TCO recursion at the "
                     ++ "fixture's input size blew the goroutine "
                     ++ "stack limit.)")

  where
    findSkyBin :: IO FilePath
    findSkyBin = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok
          then return candidate
          else do
            ok2 <- doesFileExist candidate
            if ok2
              then return candidate
              else error
                ("assertConstantStack1M: sky binary missing at "
                 ++ candidate ++ ". Run `bash scripts/build.sh` "
                 ++ "first to materialise sky-out/sky.")

    runSkyTest :: FilePath -> FilePath -> FilePath
               -> IO (ExitCode, String, String)
    runSkyTest sky testFile workDir = do
        let cp = (proc sky ["test", testFile]) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    takeDir :: FilePath -> FilePath
    takeDir p = case reverse (dropWhile (/= '/') (reverse p)) of
        ""  -> "."
        d   -> d


-- ─── Per-op fixture builder ───────────────────────────────────────


-- | Synthesise the @src/Main.sky@ + @tests/<op>StackTest.sky@ pair
-- for an op's runtime gate fixture. Returns a list suitable for
-- passing to @compileInProcessMulti@ or @assertConstantStack1M@.
--
-- This is the conventional shape per op. Callers can drop it and
-- hand-build files for ops with custom test logic.
--
-- The body string is INLINED into the test file at the place
-- marked @-- BODY GOES HERE --@; it should be a Sky expression
-- that builds + checks the rewrite's output.
buildOpFixture :: String  -- ^ op name (e.g. @"map"@)
               -> String  -- ^ Sky source of the @tests/<Op>StackTest.sky@
                          --   test file (full module, including
                          --   @module … exposing (tests)@ header).
               -> [(FilePath, String)]
buildOpFixture opName testSource =
    [ ("src/Main.sky", trivialMain)
    , ("tests/" ++ capitalise opName ++ "StackTest.sky", testSource)
    ]
  where
    trivialMain = unlines
        [ "module Main exposing (main)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Std.Log exposing (println)"
        , ""
        , "main ="
        , "    println \"cps fixture entry\""
        ]
    capitalise :: String -> String
    capitalise []     = []
    capitalise (c:cs) = toUpperC c : cs

    toUpperC :: Char -> Char
    toUpperC c
      | 'a' <= c && c <= 'z' = toEnum (fromEnum c - 32)
      | otherwise            = c


-- Suppress unused-import warning for compileInProcess so future
-- specs that import only this module can still rely on it.
_unusedCompileInProcess :: String -> IO CompileResult
_unusedCompileInProcess = compileInProcess
{-# NOINLINE _unusedCompileInProcess #-}


-- Silence unused warning for shouldBe (kept in scope for future
-- combinators in this module).
_unusedShouldBe :: Int -> Int -> Expectation
_unusedShouldBe = shouldBe
{-# NOINLINE _unusedShouldBe #-}
