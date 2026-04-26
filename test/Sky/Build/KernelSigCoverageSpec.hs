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


spec :: Spec
spec = do
    describe "Limitation #16 — dangerous-class kernel sigs registered" $ do
        -- Read the source file once for all assertions.
        let sigsFile = "src/Sky/Type/Constrain/Expression.hs"

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
