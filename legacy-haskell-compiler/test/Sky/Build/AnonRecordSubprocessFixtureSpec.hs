module Sky.Build.AnonRecordSubprocessFixtureSpec (spec) where

-- v0.17 step-2 — anon-record subprocess fixture reproduction.
--
-- Adversary-driven framing:
--
--   (a) Adversary-1 #5 — "class closure, not fixture closure."
--       The AnonRecordEmissionGuaranteeSpec single-fixture gate
--       proved a SPECIFIC fixture shape (Std.Ui.layout call
--       through a parametric Cfg widget).  But the underlying
--       leak class is broader: ANY cross-module flow of an
--       anonymous-record literal through a parametric signature
--       can hit the lazy-reset / Map.empty race.  We need
--       MULTIPLE distinct fixtures to prove the close is
--       class-shaped, not single-shape-shaped.
--
--   (b) Adversary-2 #6 — "subprocess reproduces what in-process
--       silently passes."  The in-process compile path goes
--       through 'compileEntry' / 'continueCompile' inside the
--       cabal test executable's own RTS — the IORef wipe ordering
--       changes vs a fresh subprocess fork.  In-process can
--       silently mask the leak even when the spec runs.  Forking
--       the actual built compiler binary (`sky-out/sky`) with the
--       fixture in a temp dir reproduces the production race.
--
-- TWO FIXTURES — both target the same leak class:
--
--   * iter-18 shape: a cross-module HOF whose typed cfg arg is
--     an anonymous record (the `Std.Ui.layoutWith
--     { wrapperAttrs, rootAttrs }` family).  The anon-record
--     literal is constructed at the call site in the entry
--     module; the receiving HOF lives in a dep module.
--
--   * iter-20 shape: a dep-module function whose RETURN TYPE is
--     an anonymous record.  The entry module destructures the
--     returned record and uses its fields.  Distinct from
--     iter-18 in two important ways: (1) the anon-record flows
--     in the OPPOSITE direction (dep → entry), and (2) it
--     anchors in a return-position type slot rather than a
--     callback-arg slot.
--
-- ASSERTIONS (per fixture):
--   1. `go build` succeeds (no `undefined: Anon_R_…`).
--   2. Every `Anon_R_<hash>` token referenced in emitted main.go
--      has a matching `type Anon_R_<hash> = struct{…}` decl.
--   3. At least one Anon_R_ token is present (fixture genuinely
--      exercises the path; else it would vacuously pass even
--      after a regression that drops all anon-records).
--
-- EXPECTED STATE:
--   * Pre step-3 (the architectural fix for class-wide closure):
--     EXPECTED TO FAIL on at least the iter-18 shape.  This is
--     the gate: step-3 ships when this spec turns green.
--   * Post step-3: '2 examples, 0 failures'.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..),
                      env)
import System.Environment (getEnvironment)
import System.Exit (ExitCode(..))
import Data.List (isInfixOf, nub, sort, isPrefixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run cabal install --installdir=./sky-out first")


runSky :: FilePath -> [String] -> FilePath -> [(String, String)]
       -> IO (ExitCode, String, String)
runSky sky args workDir extraEnv = do
    baseEnv <- getEnvironment
    let envList = filter (\(k, _) -> k `notElem` map fst extraEnv) baseEnv
                ++ extraEnv
    let cp = (proc sky args) { cwd = Just workDir, env = Just envList }
    readCreateProcessWithExitCode cp ""


-- Extract every "Anon_R_<hash>" token in the emitted Go source.
extractAnonTokens :: String -> [String]
extractAnonTokens = nub . sort . go
  where
    go [] = []
    go s
        | "Anon_R_" `isPrefixOf` s =
            let token = takeWhile isTokenChar s
                rest  = dropWhile isTokenChar s
            in token : go rest
        | otherwise = go (drop 1 s)
    isTokenChar c = c == '_' || (c >= 'a' && c <= 'z')
                              || (c >= 'A' && c <= 'Z')
                              || (c >= '0' && c <= '9')


-- Extract just the Anon_R_ names that have a `type Anon_R_… =` decl.
extractAnonDecls :: String -> [String]
extractAnonDecls src = nub . sort $
    [ takeWhile isTokenChar after
    | l <- lines src
    , let stripped = dropWhile (== ' ') l
    , "type Anon_R_" `isPrefixOf` stripped
    , let after = drop (length ("type " :: String)) stripped
    ]
  where
    isTokenChar c = c == '_' || (c >= 'a' && c <= 'z')
                              || (c >= 'A' && c <= 'Z')
                              || (c >= '0' && c <= '9')


-- ---------------------------------------------------------------
-- Iter-18 fixture: cross-module HOF receives an anonymous record
-- as a callback-arg slot.  Mirror of Std.Ui.layoutWith.
-- ---------------------------------------------------------------
iter18SkyToml :: String
iter18SkyToml = unlines
    [ "name = \"anon-record-iter18\""
    , "version = \"0.1.0\""
    , "entry = \"src/Main.sky\""
    ]


-- Receiving module — declares a typed HOF over an anonymous
-- record shape with two list-typed fields (callback list +
-- wrapper list).  Deliberately UNANNOTATED at the body level so
-- HM infers from the use site, matching the v0.16/0.17
-- typed-codegen path.
iter18FrameSky :: String
iter18FrameSky = unlines
    [ "module Frame exposing (apply, render)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , ""
    , "-- HOF receives an anonymous record with two list fields."
    , "-- Mirror of Std.Ui.layoutWith { wrapperAttrs, rootAttrs }."
    , "apply : { rootAttrs : List a, wrapperAttrs : List a } -> List a"
    , "apply cfg ="
    , "    List.append cfg.wrapperAttrs cfg.rootAttrs"
    , ""
    , "render xs ="
    , "    List.foldl (\\x acc -> acc ++ x) \"\" xs"
    ]


-- Entry module — call site constructs the anonymous record
-- literal.  This is the use shape that triggered iter-18 of the
-- gap-3 investigation.
iter18MainSky :: String
iter18MainSky = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , "import Frame"
    , ""
    , "main ="
    , "    let"
    , "        combined ="
    , "            Frame.apply"
    , "                { wrapperAttrs = [ \"outer-1\", \"outer-2\" ]"
    , "                , rootAttrs    = [ \"root-1\", \"root-2\" ]"
    , "                }"
    , "    in"
    , "        println (Frame.render combined)"
    ]


-- ---------------------------------------------------------------
-- Iter-20 fixture: dep-module function RETURNS an anonymous
-- record; the entry module destructures the returned record.
-- Distinct from iter-18 in that the anon-record flows from dep
-- to entry (opposite direction), and anchors a return-type slot
-- rather than a callback-arg slot.
-- ---------------------------------------------------------------
iter20SkyToml :: String
iter20SkyToml = unlines
    [ "name = \"anon-record-iter20\""
    , "version = \"0.1.0\""
    , "entry = \"src/Main.sky\""
    ]


iter20BuilderSky :: String
iter20BuilderSky = unlines
    [ "module Builder exposing (compute)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.String as String"
    , ""
    , "-- Returns an anonymous record.  Entry module destructures"
    , "-- the result via field access; codegen registers the shape"
    , "-- via synthAnonRecordName from the use site."
    , "compute : Int -> { label : String, count : Int }"
    , "compute n ="
    , "    { label = \"value=\" ++ String.fromInt n"
    , "    , count = n + 1"
    , "    }"
    ]


iter20MainSky :: String
iter20MainSky = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , "import Sky.Core.String as String"
    , "import Builder"
    , ""
    , "main ="
    , "    let"
    , "        result = Builder.compute 41"
    , "    in"
    , "        println (result.label ++ \" | next=\" ++ String.fromInt result.count)"
    ]


-- ---------------------------------------------------------------
-- Fixture writer + builder.
-- ---------------------------------------------------------------
writeFixture :: FilePath -> String -> [(String, String)] -> IO ()
writeFixture dir skyToml srcFiles = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml") skyToml
    mapM_ (\(name, contents) -> writeFile (dir </> "src" </> name) contents)
          srcFiles


buildAndCheck :: String        -- fixture label
              -> String        -- sky.toml content
              -> [(String, String)]  -- src/<name>.sky → contents
              -> IO ()
buildAndCheck label tomlContent srcFiles =
  withSystemTempDirectory ("sky-anon-subproc-" ++ label) $ \dir -> do
    writeFixture dir tomlContent srcFiles
    sky <- findSky
    (exit, stdout', stderr') <-
        runSky sky ["build", "src/Main.sky"] dir
            [("SKY_GOSIG_DIFF", "1")]
    let combined = stdout' ++ "\n" ++ stderr'

    -- Assertion 1: no `undefined: Anon_R_…` in go-build diagnostics.
    (("undefined: Anon_R_") `isInfixOf` combined) `shouldBe` False

    -- Assertion 2: build succeeded.  We report the combined output
    -- on failure so debugging is direct.
    case exit of
        ExitSuccess -> return ()
        ExitFailure n ->
            expectationFailure
                ( "iter-" ++ label ++ " build failed with exit " ++ show n
               ++ "\n--- combined output ---\n" ++ combined )

    -- Assertion 3: render-order invariant — every Anon_R_<hash>
    -- token has a matching `type Anon_R_<hash>` decl.
    mainGoExists <- doesFileExist (dir </> "sky-out" </> "main.go")
    if not mainGoExists
      then expectationFailure
             ( "iter-" ++ label ++ ": sky-out/main.go missing after build "
            ++ "(SKY_GOSIG_DIFF=1).  Build claimed success but emitted "
            ++ "no output." )
      else do
        emitted <- readFile (dir </> "sky-out" </> "main.go")
        let allTokens = extractAnonTokens emitted
            decls     = extractAnonDecls emitted
            unmatched = [ t | t <- allTokens, t `notElem` decls ]
        case unmatched of
            [] -> return ()
            xs -> expectationFailure
                    ( "iter-" ++ label ++ ": Anon_R_ tokens without "
                   ++ "matching type decl:\n  " ++ show xs
                   ++ "\nDecls present:\n  " ++ show decls
                   ++ "\nAll tokens present:\n  " ++ show allTokens )

        -- Assertion 4 (sanity): fixture genuinely exercises anon
        -- records.  Otherwise the spec would vacuously pass if
        -- codegen ever dropped all Anon_R_ shapes (which would
        -- itself be a regression in a different direction).
        (length allTokens >= 1) `shouldBe` True


spec :: Spec
spec = describe "anon-record subprocess reproduction (gap-3 class)" $ do

    it "iter-18 shape — cross-module HOF anon-record callback arg" $
      buildAndCheck "18"
        iter18SkyToml
        [ ("Main.sky",  iter18MainSky)
        , ("Frame.sky", iter18FrameSky)
        ]

    it "iter-20 shape — dep-module returns anon-record, entry destructures" $
      buildAndCheck "20"
        iter20SkyToml
        [ ("Main.sky",    iter20MainSky)
        , ("Builder.sky", iter20BuilderSky)
        ]
