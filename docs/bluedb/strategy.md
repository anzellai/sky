# BlueDB — product strategy

The decision record behind the shape in `README.md`.

## The market question

"PostgreSQL capability with document-based scalability and unlimited read/write
throughput (like DynamoDB / Firestore)." The honest constraint: you cannot
simultaneously max out linear write scaling, global serializable transactions,
strong consistency, and low latency. The design job is choosing **where to put
the knob** and letting the user turn it per operation — not pretending the
trade-off is gone.

## Native vs standalone — the resolution

**Magic-first, compat-second, one engine.**

- **Sky-native reactive auto-sync is the flagship and the moat.** It's the thing
  no SQL-first competitor can copy, and the reason to choose Sky. TAM tracks Sky
  adoption, but every unit of Sky growth compounds it.
- **A standalone engine with a Postgres-wire _read_ surface is the reach** — the
  "anyone can try it / BI connects / skeptic-converter" on-ramp. Read-only is far
  easier than full SQL read/write compliance and delivers ~all the adoption
  value, **without** letting SQL dictate the hot write path.
- **Reject SQL-first.** Leading with SQL forfeits the only unfair advantage and
  drops you into the distributed-SQL commodity war (Cockroach/Yugabyte/Neon/
  PlanetScale) where "Sky" is invisible to a `psql` user. Firebase/Convex/Zero
  prove the reactive-sync magic wins adoption **without** SQL — removing SQL was
  the feature.

Architecturally: **standalone engine, center-of-gravity Sky.** Own binary, own
protocol, own clients (so it isn't hostage to Sky's adoption and can have a
business of its own — Postgres is standalone but shines through language clients;
Convex is standalone but the magic is the integration).

The two interfaces don't fight because they serve **different jobs**: reactive
sync for app state (90% case), read/SQL for analytics/interop (escape hatch). The
worry about "inherited SQL constraints" dissolves once SQL is scoped to the
analytics job instead of owning writes.

## The workload north star (added: user, decisive)

**Fast, frequent, small read + writes.** BlueDB is an OLTP hot-path engine
first. Optimize point-op p99 latency and QPS; keep the hot path single-key /
single-range; group-commit durability; embedded-first (no network hop on the
common single-instance app); hot-key mitigation via sharded aggregates; coalesced
reactive fan-out. Analytics/scan is a separate surface that must never slow the
hot path. See `README.md` § "North star".

This maps perfectly onto the reactive Model-sync use case: a Model that mutates
on every keystroke/click/tick *is* a firehose of small point writes — so the
flagship DX and the engine's optimization target are the same workload.

## The two differentiators only Sky has

1. **Codec duality as the schema.** `Std.Codec` already turns one Sky type into
   JSON + DB mappings. Promote it into the engine: one Sky type + Codec generates
   the relational columns, the document encoding, the index layout, and the wire
   format. The relational/document duality the market wants is literally Sky's
   existing Codec, pushed to storage.
2. **Deterministic transactions in Sky.** Sky is pure/total/"if it compiles it
   works" — the ideal language for Calvin-style deterministic transactions:
   consensus orders the *commands*, deterministic Sky functions execute
   identically on every replica, no 2PC lock coordination. Sky's type system
   *gives* the determinism property those databases spend enormous runtime effort
   enforcing. This is the bet that plays to Sky's unique strength instead of
   out-optimizing Cockroach's SQL planner.

## The one risk, and its mitigation

Sky-native lead → TAM tied to Sky's adoption curve. Mitigate with (a) the
Postgres-wire read surface (non-Sky teams adopt BlueDB, get pulled toward Sky)
and (b) SkyDeploy as built-in distribution. Survivable and mitigable. Forfeiting
the moat by going SQL-first is structural and is not.

**Net: the magic is the business; SQL is the bridge.**
