-- | Single-module compilation pipeline.
-- Source → Parse → Canonicalise → Constrain → Solve → Optimise → Generate Go
module Sky.Build.Compile where

import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import qualified Data.Map.Strict as Map

import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Parse.Module as Parse
import qualified Sky.Generate.Go.Ir as GoIr
import qualified Sky.Generate.Go.Builder as GoBuilder
import qualified Sky.Sky.Toml as Toml


-- | Compilation result
data CompileResult
    = CompileOk !String            -- generated Go source code
    | CompileError !String         -- error message
    deriving (Show)


-- | Compile a single Sky file to Go source
compileFile :: Toml.SkyConfig -> FilePath -> IO CompileResult
compileFile config filePath = do
    -- Phase 1: Read source
    source <- TIO.readFile filePath
    putStrLn $ "-- Lexing " ++ filePath

    -- Phase 2: Parse
    putStrLn "-- Parsing"
    case Parse.parseModule source of
        Left err -> return (CompileError $ "Parse error: " ++ show err)
        Right modul -> do
            let modName = case Src._name modul of
                    Just (A.At _ names) -> concatMap id names
                    Nothing -> "Main"
                declCount = length (Src._values modul) + length (Src._unions modul) + length (Src._aliases modul)
            putStrLn $ "   Module: " ++ modName
            putStrLn $ "   " ++ show declCount ++ " declarations, " ++ show (length (Src._imports modul)) ++ " imports"

            -- Phase 3: Canonicalise (TODO)
            putStrLn "-- Canonicalising"

            -- Phase 4: Type Check (TODO)
            putStrLn "-- Type Checking"

            -- Phase 5: Optimise (TODO)
            putStrLn "-- Optimising"

            -- Phase 6: Generate Go
            putStrLn "-- Generating Go"
            let goCode = generateGoStub modul config
            return (CompileOk goCode)
  where
    -- Workaround for Located pattern


-- | Generate a stub Go program from a parsed module.
-- This is a placeholder until the full pipeline is wired.
generateGoStub :: Src.Module -> Toml.SkyConfig -> String
generateGoStub modul config =
    let pkg = GoIr.GoPackage
            { GoIr._pkg_name = "main"
            , GoIr._pkg_imports =
                [ GoIr.GoImport "fmt" Nothing
                , GoIr.GoImport "os" Nothing
                ]
            , GoIr._pkg_decls =
                [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                    { GoIr._gf_name = "main"
                    , GoIr._gf_typeParams = []
                    , GoIr._gf_params = []
                    , GoIr._gf_returnType = ""
                    , GoIr._gf_body =
                        [ GoIr.GoExprStmt (GoIr.GoCall (GoIr.GoQualified "fmt" "Println") [GoIr.GoStringLit "Hello from Sky (Haskell compiler)!"])
                        , GoIr.GoExprStmt (GoIr.GoCall (GoIr.GoIdent "_") [GoIr.GoQualified "os" "Args"])
                        ]
                    }
                ]
            }
    in GoBuilder.renderPackage pkg


-- | Full compilation: parse → typecheck → codegen → write → go build
compile :: Toml.SkyConfig -> FilePath -> FilePath -> IO (Either String FilePath)
compile config entryPath outDir = do
    result <- compileFile config entryPath
    case result of
        CompileError err -> return (Left err)
        CompileOk goCode -> do
            -- Write main.go
            let mainGoPath = outDir ++ "/main.go"
            writeFile mainGoPath goCode
            putStrLn $ "   Wrote " ++ mainGoPath

            -- Write go.mod
            let goModPath = outDir ++ "/go.mod"
            writeFile goModPath $ unlines
                [ "module sky-app"
                , ""
                , "go 1.21"
                ]

            putStrLn "Compilation successful"
            return (Right mainGoPath)
