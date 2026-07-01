-- | Module-level constraint generation.
-- Delegates to Expression.constrainModule which is now IO-based.
module Sky.Type.Constrain.Module
    ( constrainModule
    , constrainModuleWithExternals
    , constrainModuleWithFfi
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


-- | v0.17 close P1 step 4 — FFI-aware variant: takes BOTH the
-- cross-module externals AND the FFI-kernel signature map
-- (from @LoadedFfiTables._lft_kernelTypes@) and threads them
-- through a per-call 'Env' record's @_envFfiKernelTypes@
-- field, replacing the legacy module-level IORef read at the
-- @Can.VarKernel@ arm.
constrainModuleWithFfi
    :: Map.Map (String, String) T.Annotation
    -- ^ cross-module externals
    -> Map.Map (String, String) T.Annotation
    -- ^ FFI-kernel signatures
    -> Can.Module
    -> IO T.Constraint
constrainModuleWithFfi = ConstrainExpr.constrainModuleWithFfi
