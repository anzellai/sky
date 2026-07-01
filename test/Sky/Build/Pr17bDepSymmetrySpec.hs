{-# LANGUAGE OverloadedStrings #-}
module Sky.Build.Pr17bDepSymmetrySpec (spec) where

-- v0.17 PR-17b regression: dep-module emission symmetry — the
-- T1 leak class is closed by eager render-to-GoRaw of every dep
-- decl's body (lifting the previous 'if null depTypeParams'
-- early-out, both at the bodyExpr arm in Compile.hs:3735 and at
-- the withScopedEnclosingTypeParamsStmts helper at line ~599).
--
-- Pre-fix: a dep module that mixed ONE generic decl (typed
-- param T1) with ONE non-generic decl using Dict ops would
-- silently emit @rt.AsMapT[T1](rawData)@ inside the non-generic
-- decl, where T1 was the sibling generic's param.  Sky lowering
-- reported "success" but @go build@ rejected with
-- @undefined: T1@.
--
-- Repro fixture: tools/probe-fixtures/probe-PR17b-dep-symmetry/.
-- Lib.sky has @generic@ (typed param) and @snapshotToDict@
-- (Dict.insert-using non-generic).  Main.sky calls both.
--
-- This spec compiles the fixture clean, reads the emitted Go,
-- and asserts every @rt.AsMapT[<ident>]@ where @<ident>@ is a
-- T-var lives INSIDE a generic function header (i.e. the
-- ident is bound by an enclosing @func Name[Tn any](...@).
-- Any unbracketed @rt.AsMapT[T<n>]@ at module scope is a leak.

import Test.Hspec
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Directory (getCurrentDirectory, removePathForcibly,
                         createDirectoryIfMissing, doesFileExist)
import System.Exit (ExitCode(..))
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import qualified Data.List as List
import qualified Data.Text as T
import qualified Data.Text.IO as TIO


spec :: Spec
spec = describe "v0.17 PR-17b — dep-module symmetry — no T1 leak in mixed-arity dep" $
    it "probe-PR17b-dep-symmetry builds + emitted Go has no T-var leak outside generic headers" $ do
        cwd <- getCurrentDirectory
        let fixture = cwd </> "tools" </> "probe-fixtures"
                          </> "probe-PR17b-dep-symmetry"
            sky     = cwd </> "sky-out" </> "sky"
        skyBin    <- doesFileExist sky
        fixtureOk <- doesFileExist (fixture </> "src" </> "Main.sky")
        skyBin    `shouldBe` True
        fixtureOk `shouldBe` True
        -- Clean the fixture's per-build artefacts so this spec
        -- doesn't observe stale Go from an earlier sweep.
        removePathForcibly (fixture </> ".skycache")
        removePathForcibly (fixture </> ".skydeps")
        removePathForcibly (fixture </> "sky-out")
        let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just fixture }
        (ec, _out, _err) <- readCreateProcessWithExitCode cp ""
        ec `shouldBe` ExitSuccess
        let mainGo = fixture </> "sky-out" </> "main.go"
        mainGoOk <- doesFileExist mainGo
        mainGoOk `shouldBe` True
        body <- TIO.readFile mainGo
        let leaks = findUnboundTvarUses body
        leaks `shouldBe` []


-- Walk the emitted Go line-by-line, tracking the currently-active
-- generic-header T-var binders.  Any 'rt.AsMapT[T<n>]' or
-- 'rt.AsListT[T<n>]' that references a T-var NOT in scope is a
-- leak.  Returns the lines that contain leaks.
findUnboundTvarUses :: T.Text -> [String]
findUnboundTvarUses body =
    let
        lns = T.lines body
        results = scanLines [] lns
    in
        map T.unpack (reverse results)
  where
    -- 'scope' is the list of T-vars bound by the enclosing
    -- generic-function header.  Decls open scope on 'func X[' and
    -- close on the matching '}' at column 0.
    scanLines _ [] = []
    scanLines scope (line : rest)
        | isGenericFuncHeader line =
            let tvars = extractTvars line
            in scanLines tvars rest
        | T.null (T.strip line) || isToplevelClose line =
            scanLines [] rest
        | otherwise =
            let
                leakHere = unboundTvarUses scope line
                next    = scanLines scope rest
            in
                leakHere ++ next

    isGenericFuncHeader l =
        let s = T.strip l
        in "func " `T.isPrefixOf` s
            && (T.isInfixOf "[T" s || T.isInfixOf "[A " s)

    isToplevelClose l = T.strip l == "}"

    -- Heuristic: extract bracketed comma-separated 'TN any' /
    -- 'A any' bindings from a header.
    extractTvars line =
        let
            afterBracket = T.dropWhile (/= '[') line
            inside       = T.takeWhile (/= ']') (T.drop 1 afterBracket)
            parts        = T.splitOn (T.pack ",") inside
            bind p =
                let trimmed = T.strip p
                in T.takeWhile (/= ' ') trimmed
        in
            filter (not . T.null) (map bind parts)

    -- Find 'rt.AsMapT[X]' / 'rt.AsListT[X]' on this line.  If X
    -- looks like a T-var ('T' followed by digits) and X isn't in
    -- 'scope', report the line as a leak.
    unboundTvarUses scope line =
        let candidates =
                [ tv
                | needle <- [T.pack "rt.AsMapT[", T.pack "rt.AsListT["]
                , Just (_, rest) <- [T.breakOnEnd needle line `splitJust` needle]
                , let tv = T.takeWhile (/= ']') rest
                , looksLikeTvar tv
                ]
        in
            [ line | tv <- candidates, tv `notElem` scope ]

    looksLikeTvar tv =
        T.length tv >= 2
            && T.head tv == 'T'
            && T.all (\c -> c >= '0' && c <= '9') (T.tail tv)

    -- breakOnEnd returns (prefix, rest); we want just rest when the
    -- needle appeared.  Convert to Maybe.
    splitJust (pre, rest) needle
        | T.null pre = Nothing
        | T.takeEnd (T.length needle) pre /= needle = Nothing
        | otherwise = Just (pre, rest)
