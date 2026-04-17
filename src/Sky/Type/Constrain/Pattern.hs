-- | Pattern constraint generation.
-- Generates constraints from patterns in case branches and function parameters.
module Sky.Type.Constrain.Pattern where

-- Pattern constraints are currently handled inline in Expression.hs
-- via the patternBindings function. This module will be expanded
-- when we add full pattern exhaustiveness checking.
