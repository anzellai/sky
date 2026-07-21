module Sky.Sky.TomlTtlSpec (spec) where

import Test.Hspec
import qualified Sky.Sky.Toml as Toml


-- v0.16.19 — `[live] ttl = "24h"` (and `[auth] tokenTtl = "1h"`)
-- in sky.toml is now parsed as a Go-duration string (24 * 3600 =
-- 86400 seconds), not silently truncated to 24 SECONDS by
-- `safeReadInt`'s `reads`-based parse.
--
-- Pre-fix: `safeReadInt "24h"` returned 24 because Haskell's
-- `reads :: ReadS Int` consumes only the leading digits and
-- returns the suffix verbatim — the `[(n, _)] -> n` pattern
-- accepted the result, dropping the unit. That made every
-- Sky.Live session cookie expire after 24 seconds in production
-- as soon as the operator configured `[live] ttl = "24h"`,
-- matching the documented shape in CLAUDE.md.
--
-- Fix: `parseDurationSeconds` tries `parseGoDuration` first
-- (`Nh`/`Nm`/`Ns` plus concatenations like `1h30m`) and only
-- falls back to `safeReadInt` for bare-integer-seconds inputs.
spec :: Spec
spec = describe "Sky.Sky.Toml.parseDurationSeconds" $ do
    it "parses Nh as N * 3600 seconds (the user-visible bug)" $ do
        Toml.parseDurationSeconds "24h" 1800 `shouldBe` 86400

    it "parses Nm as N * 60 seconds" $ do
        Toml.parseDurationSeconds "30m" 1800 `shouldBe` 1800

    it "parses Ns as N seconds" $ do
        Toml.parseDurationSeconds "45s" 1800 `shouldBe` 45

    it "parses concatenated forms like 1h30m" $ do
        Toml.parseDurationSeconds "1h30m" 1800 `shouldBe` 5400

    it "parses 1h30m45s with all three units" $ do
        Toml.parseDurationSeconds "1h30m45s" 1800 `shouldBe` 5445

    it "preserves bare integers as seconds for back-compat" $ do
        Toml.parseDurationSeconds "1800" 0 `shouldBe` 1800

    it "trims surrounding whitespace before parsing" $ do
        Toml.parseDurationSeconds "  24h  " 1800 `shouldBe` 86400

    it "falls back on the empty string" $ do
        Toml.parseDurationSeconds "" 1800 `shouldBe` 1800

    it "falls back on unparseable suffixes (no silent truncation)" $ do
        -- Pre-fix this returned 24 — the regression that broke
        -- skydeploy. Confirm we DO NOT silently truncate.
        Toml.parseDurationSeconds "24junk" 1800 `shouldBe` 1800

    it "falls back when the suffix is unknown" $ do
        -- `Nd` (days) isn't supported by the runtime's parseTTL
        -- either; reject loudly rather than silently dropping the
        -- "d" and giving a 1-second TTL.
        Toml.parseDurationSeconds "1d" 1800 `shouldBe` 1800
