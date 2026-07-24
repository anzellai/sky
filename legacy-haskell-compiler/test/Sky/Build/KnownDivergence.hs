{-# LANGUAGE OverloadedStrings #-}

-- | Allowlist of expected divergences between the legacy renderer
-- chain (`solvedTypeToGo` + 6 siblings in Compile.hs) and the
-- upcoming `Sky.Type.Solve.GoTypeBuild` foundation that ships at
-- PR-5 of the v0.17 master plan.
--
-- This module is the canonical "what's left" tracker for the
-- v0.17 fully-typed-e2e refactor:
--   * Every entry MUST cite an audit gap.
--   * Every entry MUST name a "closesByShipPoint" milestone.
--   * The allowlist EMPTIES by end of Phase γ (Ship Point B at PR-17).
--   * Continue-block byte diffs do NOT go on the allowlist — they
--     are real bugs per pre-mortem lesson 4.
--
-- Doc: docs/v0.17-full-e2e-typed-master-plan.md
-- Pre-mortem: ~/.claude/.../memory/pre_mortem_v017_tco_lessons.md
module Sky.Build.KnownDivergence
    ( KnownDiv(..)
    , ShipPoint(..)
    , knownDivergences
    , isContinueBlockDivergence
    ) where

import Data.List (isInfixOf)

-- | Phase gate at which a divergence is expected to close.
data ShipPoint
    = ShipA  -- PR-8  — foundation refactor (no behaviour change)
    | ShipB  -- PR-17 — typed tuple emission (v0.17.0 headline)
    | ShipC  -- PR-19 — Cmd/Sub kernel typed
    | ShipD  -- PR-21 — FFI interface satisfaction
    | ShipE  -- PR-24 — long-tail cleanup
    deriving (Show, Eq)

-- | One documented divergence between legacy + foundation renderers.
data KnownDiv = KnownDiv
    { divProbe        :: String      -- probe directory under tools/probe-fixtures/
    , divCitation     :: String      -- audit ref or Compile.hs:line
    , divDescription  :: String
    , divClosesBy     :: ShipPoint
    } deriving (Show, Eq)

-- | The current allowlist. PR-1 ships it EMPTY — the legacy
-- renderer IS the source of truth at this point. As PR-5 lands
-- `GoTypeBuild` and we observe its first divergences from the
-- legacy chain, entries get added here with their audit citation
-- and expected close PR.
knownDivergences :: [KnownDiv]
knownDivergences = []

-- | Pre-mortem lesson 4: any divergence touching a `continue` block
-- is treated as a real bug, NOT a documented expectation. The lint
-- runs this check on every KnownDiv addition.
isContinueBlockDivergence :: String -> Bool
isContinueBlockDivergence body =
    "continue" `isInfixOf` body
