module Sky.Build.KernelSigCoverageSpec (spec) where

import Test.Hspec
import qualified Data.ByteString as BS
import qualified Data.ByteString.Char8 as BS8
import Data.List (isInfixOf)


-- Regression fence for kernel-sig coverage (CLAUDE.md Limitation
-- #16). The 9 (10 after v0.10.0 renames) "dangerous-class" sigs
-- are kernel functions that return Maybe / Result / Task wrappers
-- OR opaque FFI types (Route, Handler, HttpResponse, Decoder).
-- Without an HM sig, user pattern-matching against the wrapper
-- silently degrades to `any`, surfacing as runtime panics like
--
--     rt.AsBool: expected bool, got rt.SkyResult[interface {}, bool]
--
-- The fix lives in `lookupKernelType` in
-- src/Sky/Type/Constrain/Expression.hs. This spec asserts each
-- entry is registered by name — if a future edit accidentally
-- drops one, the spec fails before the silent runtime panic ships.


-- Source file all assertions read from. Lifted to top-level so
-- multiple `describe` blocks share the same binding.
sigsFile :: FilePath
sigsFile = "src/Sky/Type/Constrain/Expression.hs"


spec :: Spec
spec = do
    describe "Limitation #16 — dangerous-class kernel sigs registered" $ do

        it "Server.static is registered (returns opaque Route)" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            ("(\"Server\", \"static\")" `isInfixOf` body) `shouldBe` True

        it "All 4 Middleware.* sigs are registered" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            mapM_ (\name -> do
                let key = "(\"Middleware\", \"" ++ name ++ "\")"
                (key `isInfixOf` body) `shouldBe` True)
              [ "withCors", "withLogging", "withBasicAuth", "withRateLimit" ]

        it "Http.get and Http.post are registered (return Task Error HttpResponse)" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            ("(\"Http\", \"get\")" `isInfixOf` body) `shouldBe` True
            ("(\"Http\", \"post\")" `isInfixOf` body) `shouldBe` True

        it "JsonDec.map4 is registered (extends map2/map3 series)" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            ("(\"JsonDec\", \"map4\")" `isInfixOf` body) `shouldBe` True

        it "JsonDecP.custom and requiredAt are registered" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            ("(\"JsonDecP\", \"custom\")" `isInfixOf` body) `shouldBe` True
            ("(\"JsonDecP\", \"requiredAt\")" `isInfixOf` body) `shouldBe` True

        it "System.cwd and System.exit are registered (Os.* renamed in v0.10.0)" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            ("(\"System\", \"cwd\")" `isInfixOf` body) `shouldBe` True
            ("(\"System\", \"exit\")" `isInfixOf` body) `shouldBe` True

    describe "Limitation #16 mechanical sweep — bare-type kernel sigs registered" $ do
        -- Char predicates and case helpers (returns Bool / String).
        it "Char predicates: isAlpha / isDigit / isLower / isUpper" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            mapM_ (\name -> do
                let key = "(\"Char\", \"" ++ name ++ "\")"
                (key `isInfixOf` body) `shouldBe` True)
              [ "isAlpha", "isDigit", "isLower", "isUpper", "toLower", "toUpper" ]

        it "Crypto pure helpers: sha256 / sha512 / md5 / hmacSha256 / constantTimeEqual" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            mapM_ (\name -> do
                let key = "(\"Crypto\", \"" ++ name ++ "\")"
                (key `isInfixOf` body) `shouldBe` True)
              [ "sha256", "sha512", "md5", "hmacSha256", "constantTimeEqual" ]

        it "Path manipulation: base / dir / ext / isAbsolute" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            mapM_ (\name -> do
                let key = "(\"Path\", \"" ++ name ++ "\")"
                (key `isInfixOf` body) `shouldBe` True)
              [ "base", "dir", "ext", "isAbsolute" ]

        it "Math constants and trig: e / sin / cos / tan / log" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            mapM_ (\name -> do
                let key = "(\"Math\", \"" ++ name ++ "\")"
                (key `isInfixOf` body) `shouldBe` True)
              [ "e", "sin", "cos", "tan", "log" ]

        it "Time format helpers + arithmetic" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            mapM_ (\name -> do
                let key = "(\"Time\", \"" ++ name ++ "\")"
                (key `isInfixOf` body) `shouldBe` True)
              [ "format", "formatHTTP", "formatISO8601", "formatRFC3339"
              , "addMillis", "diffMillis"
              ]

        it "String pure helpers: casefold / equalFold / isEmail / isUrl / trimEnd / trimStart" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            mapM_ (\name -> do
                let key = "(\"String\", \"" ++ name ++ "\")"
                (key `isInfixOf` body) `shouldBe` True)
              [ "casefold", "equalFold", "isEmail", "isUrl", "trimEnd", "trimStart" ]

        it "Css length / transform / value helpers (Sprintf-returning)" $ do
            body <- BS8.unpack <$> BS.readFile sigsFile
            mapM_ (\name -> do
                let key = "(\"Css\", \"" ++ name ++ "\")"
                (key `isInfixOf` body) `shouldBe` True)
              [ "vh", "vw", "ch", "fr", "deg", "ms", "sec"
              , "rotate", "scale", "translateX", "translateY"
              , "calc", "minmax", "repeat"
              , "cssVar", "cssVarOr"
              , "zero", "borderBox", "systemFont"
              ]
