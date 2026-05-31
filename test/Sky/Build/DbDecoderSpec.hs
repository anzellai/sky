module Sky.Build.DbDecoderSpec (spec) where

-- v0.15.45 — typed Db.RowDecoder pipeline surface.
--
-- This spec pins:
--
--   1. Std/Db/Decode.sky exists in the stdlib tree.
--   2. Every documented combinator (string/int/float/bool/nullable/
--      succeed/fail/map/andThen/andMap/map2-5/required/optional) is
--      declared via `Ffi.kernel "DbDec_<name>"`.
--   3. A user-side decoder pipeline of the canonical shape
--      (`succeed Ctor |> andMap (string "x") |> andMap (int "y")`)
--      type-checks + builds + emits the kernel calls (not a panic
--      stub).

import Test.Hspec
import qualified Data.ByteString as BS
import qualified Data.ByteString.Char8 as BS8
import Data.List (isInfixOf)
import System.Directory (doesFileExist, createDirectoryIfMissing, getCurrentDirectory)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


spec :: Spec
spec = describe "v0.15.45 — Std.Db.Decode typed pipeline" $ do

    it "Std/Db/Decode.sky exists in sky-stdlib/" $ do
        exists <- doesFileExist "sky-stdlib/Std/Db/Decode.sky"
        exists `shouldBe` True

    it "every combinator declares via Ffi.kernel" $ do
        body <- BS8.unpack <$> BS.readFile "sky-stdlib/Std/Db/Decode.sky"
        let mustHave =
                [ "Ffi.kernel \"DbDec_string\""
                , "Ffi.kernel \"DbDec_int\""
                , "Ffi.kernel \"DbDec_float\""
                , "Ffi.kernel \"DbDec_bool\""
                , "Ffi.kernel \"DbDec_nullable\""
                , "Ffi.kernel \"DbDec_succeed\""
                , "Ffi.kernel \"DbDec_fail\""
                , "Ffi.kernel \"DbDec_map\""
                , "Ffi.kernel \"DbDec_andThen\""
                , "Ffi.kernel \"DbDec_andMap\""
                , "Ffi.kernel \"DbDec_map2\""
                , "Ffi.kernel \"DbDec_map3\""
                , "Ffi.kernel \"DbDec_map4\""
                , "Ffi.kernel \"DbDec_map5\""
                , "Ffi.kernel \"DbDec_required\""
                , "Ffi.kernel \"DbDec_optional\""
                ]
        mapM_ (\needle ->
            (needle `isInfixOf` body) `shouldBe` True) mustHave

    it "pipeline-style userDecoder type-checks + builds + routes to rt.DbDec_*" $ do
        sky <- findSky
        withSystemTempDirectory "sky-db-decoder" $ \tmp -> do
            writeDecoderFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    body <- readFile (tmp </> "sky-out" </> "main.go")
                    -- Call sites route to rt.DbDec_* kernels.
                    ("rt.DbDec_succeed" `isInfixOf` body) `shouldBe` True
                    ("rt.DbDec_andMap" `isInfixOf` body) `shouldBe` True
                    ("rt.DbDec_string" `isInfixOf` body) `shouldBe` True
                    ("rt.DbDec_int" `isInfixOf` body) `shouldBe` True
                    -- Note: the dead alias-body declarations may
                    -- carry `rt.Ffi_kernel("DbDec_...")` panic stubs
                    -- for the Layer-3 source-side bindings (e.g.
                    -- `func Std_Db_Decode_succeed() … { return
                    -- rt.Coerce[...](rt.Ffi_kernel("DbDec_succeed")) }`).
                    -- That's fine — the user code's call sites are
                    -- rewritten to the typed kernel dispatch via
                    -- Stage-4 alias resolution, which is what the
                    -- rt.DbDec_* assertions above pin.


-- ── Fixtures ──────────────────────────────────────────────────────


writeDecoderFixture :: FilePath -> IO ()
writeDecoderFixture tmp = do
    writeFile (tmp </> "sky.toml") tomlFixture
    let srcDir = tmp </> "src"
    createDirectoryIfMissing True srcDir
    writeFile (srcDir </> "Main.sky") decoderFixture
  where
    tomlFixture = unlines
        [ "name = \"db-decoder-test\""
        , "version = \"0.0.0\""
        , "[source]"
        , "root = \".\""
        ]
    decoderFixture = unlines
        [ "module Main exposing (main)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Std.Db.Decode as Decode"
        , "import Std.Log exposing (println)"
        , ""
        , "type alias User ="
        , "    { id    : Int"
        , "    , name  : String"
        , "    , email : String"
        , "    }"
        , ""
        , "userDecoder : Decoder User"
        , "userDecoder ="
        , "    Decode.succeed (\\i n e -> { id = i, name = n, email = e })"
        , "        |> Decode.andMap (Decode.int \"id\")"
        , "        |> Decode.andMap (Decode.string \"name\")"
        , "        |> Decode.andMap (Decode.string \"email\")"
        , ""
        , "-- Force the decoder to be kept by the DCE pass by referencing"
        , "-- it from `main`. We just need its shape to appear in the"
        , "-- emitted Go — actual execution is covered by the runtime-go"
        , "-- tests + the example sweep."
        , "main : Int"
        , "main ="
        , "    let _ = println \"decoder built\""
        , "        _ = userDecoder"
        , "    in 0"
        ]


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate else return "sky"


runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args dir = readCreateProcessWithExitCode
    ((proc sky args) { cwd = Just dir }) ""
