-- | Diagnostic AST + renderer regression fence (Layer 1 of v0.13).
--
-- Locks the shape of the Diagnostic value and the CLI / LSP renderer
-- output. Future migrations of error phases produce Diagnostic values
-- via the same API — these tests catch any shape drift.
module Sky.Reporting.DiagnosticSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)
import qualified Data.Aeson as A
import qualified Data.ByteString.Lazy.Char8 as BL
import qualified Sky.Reporting.Annotation as An
import Sky.Reporting.Diagnostic
import Sky.Reporting.Render
import Sky.Reporting.Lsp


spec :: Spec
spec = do
    describe "Diagnostic AST" $ do
        it "mkError produces a structured value with all fields" $ do
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "Type mismatch"
            _diag_file diag `shouldBe` "src/Main.sky"
            _diag_severity diag `shouldBe` SevError
            _diag_category diag `shouldBe` CatType
            unDiagCode (_diag_code diag) `shouldBe` "E2001"
            _diag_message diag `shouldBe` "Type mismatch"
            _diag_related diag `shouldBe` []
            _diag_hints diag `shouldBe` []

        it "withRelated appends a related region" $ do
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "Type mismatch"
                       & withRelated "src/Main.sky" sampleRegion2
                         "value originates here"
            length (_diag_related diag) `shouldBe` 1
            _rel_message (head (_diag_related diag))
                `shouldBe` "value originates here"

        it "withHint and withFix accumulate hints" $ do
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "Type mismatch"
                       & withHint "consider changing X to Y"
                       & withFix "replace X with Y" sampleFix
            length (_diag_hints diag) `shouldBe` 2
            -- Second hint has the fix.
            (_hint_suggested_fix (_diag_hints diag !! 1))
                `shouldSatisfy` (\f -> case f of Just _ -> True; Nothing -> False)

        it "hasErrors detects severity correctly" $ do
            let err  = mkError "f.sky" sampleRegion CatType typeE_Mismatch "x"
                warn = (mkError "f.sky" sampleRegion CatType typeE_Mismatch "x")
                         { _diag_severity = SevWarning }
            hasErrors [warn] `shouldBe` False
            hasErrors [err, warn] `shouldBe` True
            hasErrors [] `shouldBe` False

        it "diagnostic codes are stable strings" $ do
            -- Locking: tests grep by code, never wording. If these
            -- strings change, ALL consumers (CI scripts, docs, LSP
            -- code-action handlers) break. Don't change without a
            -- deprecation path.
            unDiagCode parseE_SyntaxError       `shouldBe` "E0001"
            unDiagCode canonE_UndefinedName     `shouldBe` "E1001"
            unDiagCode typeE_Mismatch           `shouldBe` "E2001"
            unDiagCode exhaustE_NonExhaustive   `shouldBe` "E3001"
            unDiagCode codegenE_TypedKernelAnyArg `shouldBe` "E4001"
            unDiagCode goE_BuildFailed          `shouldBe` "E5001"

    describe "CLI renderer" $ do
        it "renders header with severity + location + code" $ do
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "Type mismatch"
            out <- renderCli diag
            out `shouldSatisfy` (\s -> "ERROR" `isInfixOf` s)
            out `shouldSatisfy` (\s -> "src/Main.sky:5:10" `isInfixOf` s)
            out `shouldSatisfy` (\s -> "[E2001]" `isInfixOf` s)

        it "renders the message body" $ do
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "Type mismatch here"
            out <- renderCli diag
            out `shouldSatisfy` (\s -> "Type mismatch here" `isInfixOf` s)

        it "includes hints when present" $ do
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "X"
                       & withHint "try Y"
            out <- renderCli diag
            out `shouldSatisfy` (\s -> "Hint: try Y" `isInfixOf` s)

        it "shows related region details" $ do
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "X"
                       & withRelated "src/Other.sky" sampleRegion2
                         "value defined here"
            out <- renderCli diag
            out `shouldSatisfy` (\s -> "Related at src/Other.sky:" `isInfixOf` s)
            out `shouldSatisfy` (\s -> "value defined here" `isInfixOf` s)

    describe "LSP serialiser" $ do
        it "produces valid JSON with the LSP-required fields" $ do
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "Type mismatch"
                json = renderLspDiagnostic diag
                bs = BL.unpack (A.encode json)
            -- Check required keys appear in encoded JSON.
            ("\"range\"" `isInfixOf` bs) `shouldBe` True
            ("\"severity\":1" `isInfixOf` bs) `shouldBe` True
            ("\"code\":\"E2001" `isInfixOf` bs) `shouldBe` True
            ("\"source\":\"sky" `isInfixOf` bs) `shouldBe` True

        it "converts 1-based positions to 0-based for LSP" $ do
            -- Region line=5, col=10 → LSP line=4, character=9
            let diag = mkError "src/Main.sky" sampleRegion
                         CatType typeE_Mismatch "x"
                bs = BL.unpack (A.encode (renderLspDiagnostic diag))
            ("\"line\":4" `isInfixOf` bs) `shouldBe` True
            ("\"character\":9" `isInfixOf` bs) `shouldBe` True

        it "saturates negative positions at 0" $ do
            -- Synthetic region with line=0 should saturate, not
            -- produce line=-1 (which the LSP rejects).
            let synth = An.Region (An.Position 0 0) (An.Position 0 0)
                diag = mkError "src/Main.sky" synth
                         CatType typeE_Mismatch "x"
                bs = BL.unpack (A.encode (renderLspDiagnostic diag))
            ("\"line\":-1" `isInfixOf` bs) `shouldBe` False
            ("\"character\":-1" `isInfixOf` bs) `shouldBe` False

        it "groups diagnostics by file in renderLspMany" $ do
            let d1 = mkError "src/A.sky" sampleRegion CatType typeE_Mismatch "x"
                d2 = mkError "src/B.sky" sampleRegion CatType typeE_Mismatch "y"
                d3 = mkError "src/A.sky" sampleRegion2 CatType typeE_Mismatch "z"
                bs = BL.unpack (A.encode (renderLspMany [d1, d2, d3]))
            -- Both files should appear at top level
            ("src/A.sky" `isInfixOf` bs) `shouldBe` True
            ("src/B.sky" `isInfixOf` bs) `shouldBe` True


-- ─── fixtures ────────────────────────────────────────────────────────

sampleRegion :: An.Region
sampleRegion = An.Region (An.Position 5 10) (An.Position 5 20)

sampleRegion2 :: An.Region
sampleRegion2 = An.Region (An.Position 8 4) (An.Position 8 10)

sampleFix :: SuggestedFix
sampleFix = SuggestedFix
    { _fix_description = "Replace X with Y"
    , _fix_edits =
        [ TextEdit { _edit_file = "src/Main.sky"
                   , _edit_region = sampleRegion
                   , _edit_newText = "Y" }
        ]
    }


-- mini reverse-application operator (avoiding Data.Function import)
(&) :: a -> (a -> b) -> b
x & f = f x
infixl 1 &
