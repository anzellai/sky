# Audit-grade diagrams — `sky doc --diagram`

> Goal: every diagram Sky emits is a real, submittable SOC2 / ISO 27001 artefact,
> not a source-tree picture. The unfair advantage: a Sky app is
> `update : Msg -> Model -> (Model, Cmd Msg)` — pure, total, exhaustively matched
> — so the whole behaviour is STATICALLY decidable. The compiler can enumerate
> every user action, every branch, every effect, every reachable state, and every
> follow-up message. No external tool can do this for a normal codebase.

## The problem with the current suite

`components` maps the SOURCE TREE (module × generic capability): half its rows are
TEA plumbing (`Msg`, `Subs`, `View.*`), and "Database"/"External HTTP" never name
the store or the service. `wire` and `journey` already extract real API + action
facts, but as flat tables, and with no trust boundaries, data classification, or
named external systems — the three things an auditor actually scores.

## The behaviour graph (the foundation every diagram reads)

One shared analysis over the HIR builds a directed multigraph:

- **Nodes:** Pages (states the user sees) + external systems + data stores.
- **Action edges:** `Page --[Msg]--> {effects, /_rpc endpoint, store touched, next Page}`,
  where the Msg is grounded in the page's VIEW (which Msgs its handlers dispatch).
- **Navigation edges:** a branch writing `page = NextPage`.
- **Continuation edges:** a branch's `Cmd.perform task ToMsg` → the follow-up Msg.

This is complete and provable: it is every `Msg × update` branch plus every
view-emitted action, with dead actions and unreachable states falling out for free.

### Statically-derived overlays (the compiler's advantage)

- **Trust boundaries:** browser wasm client | `/_rpc` server | data store | third
  party — from the Sky.Spa split and each effect's origin.
- **Data classification:** a flow is CONFIDENTIAL when it carries a
  `Sky.Core.Secret.Secret`, a `Std.Auth` session/cookie, or a PII-shaped field —
  Sky's type system knows this statically.
- **Named external systems:** from the literal `Http` call targets +
  `Std.Auth` OAuth provider config, not a generic "External HTTP".
- **Auth / attack surface:** CSRF-exempt endpoints and unguarded routes, flagged.

## The suite — each artefact maps to a named control

| `--diagram` | Artefact | Maps to |
|---|---|---|
| **`flow`** (replaces `journey`) | Data-flow diagram: actor → page → action → effect/RPC → store/external, in trust-boundary lanes, confidential flows marked | SOC2 CC3 (risk/DFD), CC6 (logical access); ISO A.8 (classification), A.13 (comms security) |
| **`components`** (reworked) | C4 container / system-architecture diagram: the ~4 real containers (browser client, SSR backend, PostgreSQL, each external system) + trust zones + protocols; source modules collapse to an appendix | SOC2 system description; ISO A.14 (secure architecture) |
| **`wire`** (enhanced) | API + authentication call-paths: method, path, auth requirement (CSRF-exempt flagged), guard, request/response shapes, effects, store | SOC2 CC6/CC7; ISO A.9 (access control), A.14 |
| **`telemetry`** (enhanced) | Audit-logging & monitoring surface: what the app logs / meters / traces | SOC2 CC7 (monitoring); ISO A.12.4 (logging) |
| **`callpath`** (build the planned one) | Per-endpoint end-to-end trace: entry → CSRF/guard → handler → effects → store | SOC2 CC6; ISO A.14 |

### Companion evidence tables (not diagrams)

- **Data inventory:** each store × record types held × classification (any Secret /
  auth / PII field). → ISO A.8 asset inventory.
- **Sub-processor list:** every named external system the app calls, from HTTP
  targets. → SOC2 CC, ISO A.15 (supplier relationships).

## Build order

1. **Behaviour-graph analysis** (`analyze_flow` over the HIR): the three new edge
   types (view→Msg, navigation, continuation) on top of the existing per-branch
   effect/read/write extraction. Plus the `flow` renderer (SVG + puml + md).
   Replaces `journey`.
2. **Compliance overlays:** trust boundaries + data classification + named
   external systems; rework `components` into the C4 container view.
3. **`callpath`** + the machine-readable JSON export (so the graph can drive
   external audit tooling) + the two evidence tables.

Every renderer reads the ONE behaviour graph, so the artefacts stay consistent.

## Locked decisions (2026-09-15)

The bar: a user runs one command and submits the output to a SOC2 / ISO 27001
auditor unchanged. So:

- **Global-chrome split.** An action reachable from EVERY page is global chrome
  (nav / shared layout), listed once in a "Global actions" section; page-unique
  actions sit under their page. This fixes the shared-layout over-attribution and
  reads as "on this page a user can …".
- **The submittable artefact is the SVG, drawn as a trust-boundary swimlane DFD.**
  Lanes: Browser client (wasm) · `/_rpc` server · Data stores · External systems.
  Data-flow arrows cross the lanes; confidential flows are marked; a legend and an
  `app-name · generated <date>` title are always present. Markdown stays the
  detailed per-page evidence list; PlantUML mirrors the SVG.
- **Overlays are always on** (no flag): trust boundaries, data classification
  (a flow is CONFIDENTIAL when it carries a `Secret`, a `Std.Auth` session/cookie,
  or a PII-named field), and named external systems (real hosts from the HTTP call
  literals, which also produce the sub-processor list).
- **One audit bundle.** `sky doc --diagram audit --out <dir>` writes the whole
  suite (every diagram as SVG + md, plus the data-inventory and sub-processor
  tables) so a user hands an auditor a folder, not four commands.
