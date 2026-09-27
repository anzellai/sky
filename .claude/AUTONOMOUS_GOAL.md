# AUTONOMOUS GOAL (captured verbatim 2026-09-27)

> i'd like not that many releases...
> so using worktree or branch for individual phase... with the end goal to release ONLY 0.27.0, covering 1-6 perhaps even 1-7.
>
> all fully e2e 100% unattended + autonomous + PIV.
> no inputs from me, you may proceed now.

## Scope: the phases "1-6 (perhaps 1-7)" refer to

The plan presented just before the mandate, built from a downstream app's report
of 22 Sky gaps, six design studies and three adversarial reviews. Go FFI stays
`Result Error a` (decided earlier by the user).

1. **Security.** Sky.Live session-id rotation on every change of bound user
   (all tabs follow, durable snapshot moves); revocation also deletes the
   durable snapshot; Sky.Spa RPC as a cookie-authenticated route kind with an
   Origin / Sec-Fetch-Site check and JSON-only bodies, Secure cookie on HTTPS;
   an authorisation guard for Sky.Spa `GET /_sky/sub`; dev WebSocket origin
   check plus a loopback Host guard on the dev listener (anti DNS rebinding,
   `SKY_ALLOWED_HOSTS`); CSRF exemptions keyed by method and path; start-up
   bind-address line; `Server.withHeader` copy-on-write and tests.
2. **Small correctness and ergonomics.** `Task.loop` / `Step` / `Task.forever`;
   `sky test` exits 2 on a build failure; `Result.toMaybe`, `Sky.Core.Tuple`,
   `Json.Decode.value`, `Json.Encode.raw` (and no silent `""` on encode
   failure); WebSocket server frame type and a Task-based client `receive`
   (plus the sessionless reaper fix); an embedded Sky.Live mode (no signal
   handling, no process exit); `Task.spawn` logs panics.
3. **FFI `Result` enforced by the type checker**, delivered through the type
   database to every inference path, lazily parsed, strict on the wrapper,
   arity and primitives, wildcards for Go-opaque types; `Sky.Ffi` escape hatch
   closed or typed; partial application eta-expanded.
4. **Sky.Live platform.** Widget islands with typed messages (Live and Spa);
   `App.serve` with a stop handle; opt-in sessions without cookies.
5. **Stdlib modules.** Crypto primitives (Ed25519, X25519, HKDF, XChaCha),
   QR encode, process spawning with streams and PTYs, file watching, Noise IK,
   a PAKE.
6. **UI and tooling.** `Ui.text` wrapping; `--format json` diagnostics; local
   Go path dependencies for `sky add`; native permission text, entitlements,
   secure storage, biometrics, release packaging; `Std.Ui.Canvas`; a terminal
   element.
7. **(Optional) full Task trampoline**, with the review's corrections.

## Definition of done

- One release only: **v0.27.0**, tagged once, covering phases 1 to 6 at least.
- Each phase on its own branch or worktree, merged into the v0.27.0 integration
  branch after its own verification.
- PIV per phase (CLAUDE.md §0.4): Plan (architecture consult, adversarial
  review for compiler and security work) → Implement (regression test first)
  → Verify (narrow gates per change, the release workflow's full suite before
  merge and tag, real apps behind Caddy).
- Fully unattended: design decisions the user would normally make are taken
  with the safest reasonable default and recorded in the CHANGELOG.
- "Done" is declared only by a fresh-context Judge agent against this verbatim
  goal (CLAUDE.md §0).
