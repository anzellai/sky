-- | Module-level constraint generation.
-- Delegates to Expression.constrainModule which is now IO-based.
module Sky.Type.Constrain.Module
    ( constrainModule
    , constrainModuleWithExternals
    )
    where

import qualified Data.Map.Strict as Map
import qualified Sky.AST.Canonical as Can
import qualified Sky.Type.Type as T
import qualified Sky.Type.Constrain.Expression as ConstrainExpr


-- | Generate constraints for an entire module (IO for fresh names)
constrainModule :: Can.Module -> IO T.Constraint
constrainModule = ConstrainExpr.constrainModule


-- | Cross-module-aware variant: seeds the solver with external
-- signatures keyed by (home, name) so VarTopLevel references
-- to imported values emit CForeign with the external annotation
-- instead of falling back to a fresh TVar.
constrainModuleWithExternals
    :: Map.Map (String, String) T.Annotation
    -> Can.Module
    -> IO T.Constraint
constrainModuleWithExternals = ConstrainExpr.constrainModuleWithExternals
