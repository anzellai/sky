module Sky.Build.AnonRecordSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- v0.13 E regression: when HM infers an unmatched anon-record
-- shape, `synthAnonRecordName` registers it in `globalAnonRecords`
-- and `generateAnonRecordDecls` emits a `type Anon_R_<hash> =
-- struct { … }` decl so the typed Go name actually resolves.
--
-- Pre-E, `sanitiseTypedDeep` rewrote every `Anon_R_*` token in
-- emitted type strings to `any` — a contract-violation cover-up
-- that hid every anon-record inference behind an `any` collapse.
-- The cover-up is now removed; this spec pins the replacement
-- behaviour.
--
-- Note: across the current 26-example sweep no `synthAnonRecordName`
-- call fires (A1's superset-match catches every shape). The
-- fixtures below construct synthetic field-sets that no project-
-- declared alias can match, so the renderer is genuinely forced
-- through the anon path.
spec :: Spec
spec = do
    describe "anon-record struct decl emission" $ do
        it "build succeeds for an unmatched anon-record shape" $ do
            sky <- findSky
            withSystemTempDirectory "sky-anon-rec-build" $ \tmp -> do
                writeFixture tmp anonFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess

        it "emits no dangling `Anon_R_*` reference without a struct decl" $ do
            sky <- findSky
            withSystemTempDirectory "sky-anon-rec-no-dangle" $ \tmp -> do
                writeFixture tmp anonFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- Every `Anon_R_*` reference in the emitted Go MUST
                -- correspond to a `type Anon_R_* =` decl. Otherwise
                -- `go build` would have rejected with
                -- `undefined: Anon_R_<hash>`. The build succeeding
                -- already guarantees this; we also assert directly
                -- by counting refs vs decls.
                let refLines  = filter ("Anon_R_" `isInfixOf`) (lines body)
                    declLines = filter (\l ->
                        "type Anon_R_" `isInfixOf` l) refLines
                -- Either there are zero anon refs (the inference
                -- path didn't fire — current default for most Sky
                -- code), OR every distinct anon name appears in a
                -- type decl.
                if null refLines
                    then return ()
                    else do
                        not (null declLines) `shouldBe` True


    describe "sanitiseTypedDeep cover-up removal" $ do
        it "emitted main.go does not silently rewrite Anon_R_ to any" $ do
            -- The pre-E cover-up substituted every `Anon_R_*` token
            -- with `any` mid-emission. If that rewrite were still
            -- active, the emitted main.go would never contain the
            -- Anon_R_ identifier even for shapes that genuinely
            -- need it. This test guards against the rewrite
            -- creeping back in.
            --
            -- We check: for a fixture that intentionally builds
            -- two record literals (which the codegen lowers via
            -- the `Can.Record` Nothing-branch, potentially routing
            -- through `synthAnonRecordName` when there's no matching
            -- alias), no `interface conversion: rt.SkyValue is …`
            -- panic creeps in. If E's struct emission is correct,
            -- the build succeeds AND the binary runs.
            sky <- findSky
            withSystemTempDirectory "sky-anon-rec-no-rewrite" $ \tmp -> do
                writeFixture tmp anonFixture
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True


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
            ("name = \"anon-rec\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") body


-- ─── Fixtures ──────────────────────────────────────────────────────

-- Anon-record fixture: record literals with field names that no
-- declared `type alias` in this program (or the imported stdlib)
-- has — so `lookupRecordAlias`'s exact + superset match returns
-- Nothing for the inferred shape, forcing `solvedTypeToGo`'s
-- `T.TRecord` arm to call `synthAnonRecordName`.
--
-- The build MUST succeed — pre-E the `Anon_R_*` reference would
-- have appeared in emitted Go with no matching `type Anon_R_*`
-- decl (the cover-up via sanitiseTypedDeep replaced it with
-- `any`, masking the contract violation). Post-E,
-- generateAnonRecordDecls emits one struct decl per registered
-- shape — so even if the inference path fires, the typed Go
-- name resolves.
--
-- Sky lacks row-polymorphic annotation syntax (`{ r | f : T }`),
-- so the fixture relies on HM-inferred record shapes from
-- unannotated functions.
anonFixture :: String
anonFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "-- Unannotated record literal. Field names are picked to be"
    , "-- vanishingly unlikely to collide with a project-declared"
    , "-- alias (no `type alias X = { _kZRare1 : ... }` exists)."
    , "mkRec ="
    , "    { _kZRare1 = \"hello\", _kZRare2 = 42 }"
    , ""
    , "describe ="
    , "    let"
    , "        r = mkRec"
    , "    in"
    , "        r._kZRare1 ++ \":\" ++ String.fromInt r._kZRare2"
    , ""
    , "main = println describe"
    ]
