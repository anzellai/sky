# AUTONOMOUS GOAL — Sky.Spa (branch `exp/spa`)

## Verbatim mandate (user, 2026-08-21)

> ok great we're aligned, you have enough context and understanding from what
> i'm chasing... DX + scalability + maintenance + performance + security cannot
> be compromised.
>
> be in fully autonomous mode (agents if needed) + grill + PIV mode.

Prior turn (the aligned direction, also verbatim intent):

> the end goal is indeed cross platform view rendering. so straight to a client
> only renderer is the key ... make a new lib following sky.live, but it's
> sky.spa ... if we look at sky update ast tree we know already which update is
> effect task based ... why do we need the extra API routes? ... essentially
> still sky.live just smart logics ... start with a new branch exp/spa ... wasm
> ... can also target mobile apps.

## What "done" means (the aligned architecture — the Judge verifies the LITERAL claim)

**Sky.Spa = the TEA loop, statically partitioned, running client-side.** One Sky
program; the compiler decides what runs where.

1. **WASM core + per-platform renderer.** The Model + pure `update` + `view →
   Element` compile to a **portable WASM logic core**; a thin per-platform
   renderer paints the `Element` (DOM on web via a small JS glue; `Element →
   native` bridge for mobile later). The expensive/typed part is shared; the
   renderer is the only per-platform shim. Reuses Sky's existing
   renderer-agnostic `Std.Ui.Element`.
2. **Source-of-truth split.** Model = `{ ui, data }`. `ui` = client-owned
   ephemeral state (never serialized, no codec). `data` = a cached projection of
   server truth (has a `Std.Codec`, crosses the wire). "Has a codec ⇒
   server-backed" is the boundary.
3. **AST-derived client/server boundary — no hand-written API routes.** Per
   `update` branch the compiler knows: effect target (pure / client-effect /
   server-effect), read-set, write-set. Pure branches run client-side with zero
   round-trip; server-effect branches become an auto-generated RPC that sends the
   read-set and returns the write-set delta.
4. **Compile-time-verified split (the thesis / "if it compiles it works" across
   the wire).** A Model field written by BOTH a client-pure branch AND a
   server-effect branch is a *split conflict* the compiler rejects (or forces a
   declared merge policy). If it compiles, the client/server state split is sound
   and the sync cannot clobber.
5. **Stateless backend.** The server holds NO per-user Model, NO session, NO SSE
   — it authenticates + executes server effects + owns durable `data`. Scales
   like any stateless API (millions of concurrent; DB is the only shared axis).
6. **Untrusted client is a first-class rule.** `update` runs on the user's
   machine → the server re-validates/re-authorizes every server-effect and
   re-reads authoritative data from the DB; never trusts a client-sent value for
   anything security-relevant. This is unavoidable in the generated boundary.

### The five pillars — none may be compromised (Judge checks each)

- **DX** — a Sky.Spa app is written like a Sky.Live app (same `Model/Msg/update/
  view` over `Element`); the split is compiler-derived, not hand-plumbed.
- **Scalability** — stateless backend + client-held state → horizontal, no
  sticky/SSE/session-store ceiling.
- **Maintenance** — one language, one type system, one `Element` view, shared
  `Codec` wire contract; no TS/OpenAPI drift.
- **Performance** — pure UI transitions are client-local (zero round-trip);
  server calls are batched/on-demand; view cost is the client's, not a shared
  fleet's.
- **Security** — untrusted-client rule enforced at the generated boundary;
  server re-validates; Sky's typed secrets + `Std.Auth` + prod gate carry over.

## First artifacts (de-risk before the big compiler work)

1. **Feasibility spike** — can Sky → Go → **WASM** (`GOOS=js GOARCH=wasm`)
   compile + run a minimal TEA loop in a browser? This is the riskiest
   assumption (the runtime pulls in postgres/file/net that may not target
   `js/wasm`). The result scopes whether a **WASM-compatible runtime subset** is
   needed and how large.
2. **`docs/skyspa/design.md`** — the design above, written against the REAL
   `Std.Ui.Element` / `Sky.Core.Task` / `Std.Codec` / lower+codegen surfaces, so
   it is concrete, not aspirational. Centrepiece = the read/write-set boundary +
   the compile-time disjointness check.
3. **A running counter spike** — Model/`update`/`view` → WASM core → a small
   `Element → DOM` renderer, proving the client loop + renderer end-to-end, with
   a real bundle-size + interop number (WASM-vs-JS decided on evidence).

## PIV protocol (per CLAUDE.md §0, §0.3, §0.4)

Architecture-Consult (cite the real Sky files; is the tactic sound?) → adversarial
Grill (where does it break — runtime WASM-incompat, bundle size, DOM interop,
security, the split soundness) → Implement in additive phases → fresh-context
Judge verifies the LITERAL claims + the five pillars. Forbidden in a PASS verdict:
"but / except / however / caveat / mostly / essentially / for the scope of /
modulo". Exploratory: a genuine "the runtime cannot target wasm without X" is a
finding to report + scope, not a failure — but it is reported honestly, not
glossed.
