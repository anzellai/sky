-- | Module-level constraint generation.
-- Generates constraints for all declarations in a module.
module Sky.Type.Constrain.Module
    ( constrainModule
    )
    where

import qualified Data.Map.Strict as Map
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Type.Type as T
import qualified Sky.Type.Constrain.Expression as ConstrainExpr


-- | Generate constraints for an entire module
constrainModule :: Can.Module -> T.Constraint
constrainModule canMod =
    constrainDecls Map.empty (Can._decls canMod)


-- | Generate constraints for a declaration chain
constrainDecls :: ConstrainExpr.Env -> Can.Decls -> T.Constraint
constrainDecls env decls = case decls of
    Can.SaveTheEnvironment ->
        T.CTrue

    Can.Declare def rest ->
        let defCon = ConstrainExpr.constrainDef env def
            (name, defType) = defTypeInfo def
            env' = Map.insert name (T.Forall [] defType) env
            restCon = constrainDecls env' rest
        in T.CAnd [defCon, restCon]

    Can.DeclareRec def defs rest ->
        let allDefs = def : defs
            defInfos = map defTypeInfo allDefs
            recEnv = foldr (\(n, t) e -> Map.insert n (T.Forall [] t) e) env defInfos
            defCons = map (ConstrainExpr.constrainDef recEnv) allDefs
            restCon = constrainDecls recEnv rest
        in T.CAnd (defCons ++ [restCon])


-- | Get name and type from a definition
defTypeInfo :: Can.Def -> (String, T.Type)
defTypeInfo (Can.Def (A.At _ name) params _body) =
    let paramTypes = zipWith (\i _ -> T.TVar ("_def_arg" ++ show i)) [0::Int ..] params
        resultType = T.TVar ("_def_result_" ++ name)
    in (name, foldr T.TLambda resultType paramTypes)
defTypeInfo (Can.TypedDef (A.At _ name) _freeVars typedPats _body retType) =
    let funcType = foldr (\(_, ty) acc -> T.TLambda ty acc) retType typedPats
    in (name, funcType)
