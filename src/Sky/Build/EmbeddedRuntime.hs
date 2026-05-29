{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}

-- | The Sky Go runtime (runtime-go/) and Sky-source stdlib (sky-stdlib/)
-- bundled into the sky binary at build time via Template Haskell.
-- Released binaries are fully standalone — no on-disk runtime-go/ or
-- sky-stdlib/ required.
--
-- Issue #58: empirical bug found 2026-05-18 — `embedDir` from
-- `file-embed-0.0.16.0` does NOT recurse into subdirectories when
-- TH-spliced into a cabal-installed binary (works correctly in
-- `runghc`, so the bug is in optimised-compile path / TH state
-- interaction). The compiled binary's `embeddedRuntime` had 97
-- entries (only top-level + go.mod/go.sum) instead of the 114 on
-- disk — `rt/jobs/*.go` and `rt/telemetry/*.go` were silently
-- missing. Any user app would `go build` fail with `package
-- sky-app/rt/jobs is not in std`.
--
-- Workaround: use a custom TH splice that explicitly walks the
-- tree at TH compile time and emits the full file list (rather
-- than trusting `embedDir`'s recursive walk).
--
-- Audit P3-3: cabal must re-embed when runtime / stdlib files
-- change. `embedDirRecursive` calls `qAddDependentFile` on every
-- file, but cabal does NOT track non-`.hs` files for its own
-- up-to-date check — so editing a `.sky` / `.go` file alone (even
-- with a newer mtime) does NOT make `cabal build` recompile this
-- module, and the embedded copy goes stale. To force a re-embed,
-- make a real content change to THIS `.hs` file — bump the marker
-- below — so cabal recompiles it and the splice re-walks the tree.
--
-- re-embed marker: 2026-05-27 — Cycle 4 HS (rev7): JS comment </script> literal escaped + stream-loop session stamp
-- re-embed marker: 2026-05-28 — Cycle 4 PT: Std.PubSub.publish (Task-shaped) + Std/PubSub.sky stdlib + live_pubsub_task.go runtime
-- re-embed marker: 2026-05-28b — Sky.Http.Server withHeader Content-Type override fix (spike-discovered)
-- re-embed marker: 2026-05-28c — fix(http-server): Server.static implementation via http.FileServer (was a stub returning literal "static:dir")
-- re-embed marker: 2026-05-28e — Cycle 4 #353: sky fmt next-anchor fallback so body comments above a reflowed expression round-trip losslessly
-- re-embed marker: 2026-05-28f — revert(canonicalise): roll back #350 alias-name fix (regression in row-poly access on duplicate-named modules — #361)
-- re-embed marker: 2026-05-28g — fix(canonicalise): cross-module alias-name collision v2 — (home, name) primary lookup + unique-body bare-name fallback (#350 + #361)
-- re-embed marker: 2026-05-29 — Cycle 4 NE (#359): Cmd.publishNoEcho + PubSub.publishNoEcho — opt-out echo via SessionEvent.SkipOrigin + Broker.SubscribeWithOwner
module Sky.Build.EmbeddedRuntime
    ( embeddedRuntime
    , embeddedSkyStdlib
    ) where

import Data.ByteString (ByteString)
import Sky.Build.EmbedDirTH (embedDirRecursive)


embeddedRuntime :: [(FilePath, ByteString)]
embeddedRuntime = $(embedDirRecursive "runtime-go")


embeddedSkyStdlib :: [(FilePath, ByteString)]
embeddedSkyStdlib = $(embedDirRecursive "sky-stdlib")
