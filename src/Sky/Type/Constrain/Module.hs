-- | Module-level constraint generation.
-- Delegates to Expression.constrainModule which is now IO-based.
module Sky.Type.Constrain.Module
    ( constrainModule
    )
    where

import qualified Sky.AST.Canonical as Can
import qualified Sky.Type.Type as T
import qualified Sky.Type.Constrain.Expression as ConstrainExpr


-- | Generate constraints for an entire module (IO for fresh names)
constrainModule :: Can.Module -> IO T.Constraint
constrainModule = ConstrainExpr.constrainModule
