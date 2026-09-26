# v0.25.20 draft notes — Sky Console live data and the live channel

Draft for the v0.25.20 CHANGELOG section (branch `fix/console-live`). Fold it in
at the cut; this file is not a CHANGELOG heading.

### Fixed

- **The Sky Console shows live data again when console auth is on.** With
  `SKY_CONSOLE_AUTH=token` or `app` (and always under `ENV=production`) the
  console header stayed on "Sky — · dev · uptime 0s" and every panel stayed
  empty (v0.25.17 – v0.25.19). The console's reads of `/_sky/console/api/*` did
  not send the internal token that the API requires, so every read was refused,
  and the failure was not shown. The console now sends the token, a failed read
  is shown under the header ("Telemetry read failed: …"), and the header says
  "waiting for telemetry" until the first read arrives.
- **Build identity is real.** `sky build` now stamps the app binary with the
  compiler version, the project commit and the build time. The console header
  and `/_sky/buildinfo` showed `dev` / `dev` / `unknown` before. Set
  `SKY_BUILD_COMMIT` for a build with no `.git` (for example a Docker context)
  and `SKY_BUILD_EPOCH` (Unix seconds) for a reproducible build time. An
  `-ldflags` already in `GOFLAGS` is kept, and then no stamp is written.
- **Streams are no longer cut every 30 s on a `Server.listen` host** (every
  `Sky.Http.Server` app and every `Sky.Spa` backend). The server's 30 s read and
  write deadlines cut the console's live channel, `Sky.Http.Server.Stream`
  responses (the Sky.Spa push topic) and WebSockets in the middle of the body.
  Behind a proxy this showed as "aborting with incomplete response … unexpected
  EOF" (Caddy) and `net::ERR_HTTP2_PROTOCOL_ERROR`, with the page on
  "Reconnecting" about every 33 s. Each stream now lifts the deadlines for its
  own connection.
- **A page whose session is gone recovers by itself.** After a restart with the
  `memory` session store (every redeploy), on a replica that never saw the
  session, after expiry, or when the console login refuses the stream, the
  live channel now gets one classified `session-lost` event. The page shows
  "Session ended. Reloading…" and reloads once, which starts a new session or
  shows the login form. If a reload does not help (a third loss in 60 s), the
  page stops and says so, instead of reconnecting for ever. The server logs
  each case at info as `live.sse.session_lost` with its reason. This applies to
  every Sky.Live page, not only the console.
- **One console login works across processes.** The console cookie key now
  depends on `SKY_CONSOLE_TOKEN` only. Two upstream slots, or the old and new
  process of a redeploy, accept the same console login. Before, a tab that
  moved to the other process was refused.

### Removed

- The unused Go-side console data bridge (`hydrateInitialModel` and its
  helpers in `runtime-go/rt/console_app`). Nothing called it; its test passed
  while the real data path was broken.

### Tests

- `runtime-go/rt/console_app/console_live_data_test.go`,
  `runtime-go/rt/live_sse_session_lost_test.go`, a console cookie-key test, and
  build-stamp tests in `rust/crates/project`.
- `scripts/console-live-e2e.sh`: the console in a real browser, for a Sky.Live
  app, a Sky.Spa backend and an analytics fixture, directly and behind a real
  Caddy (HTTPS, HTTP/2, `encode zstd gzip`, `flush_interval -1`, two upstream
  slots, strict CSP). It checks live values, the Logs, Traces and Analytics
  tabs, 40 s with no "Reconnecting", and recovery from a backend restart. It
  runs in the nightly sweep and in the release `gate-web` job, and it fails on
  v0.25.19.
