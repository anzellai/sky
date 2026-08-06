# BlueDB Phase 3 — status

> Tracks the phased sub-plan in `phase3-api-design.md` §7. Phase 3a is the Go embedded core +
> the SSI-wiring soundness, provable with Go tests. Phases 3b and 3c are the Sky port + the SQL
> adapters/parity.

## Phase 3a — Go `Backend` + embedded adapter + real indexer + SSI-wiring soundness — **DONE**

Landed in `runtime-go/bluedb/`. Builds `go build ./bluedb/...`, cross-compiles
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`, `go vet` clean, `go test ./bluedb/ -race` green
(64 prior + 12 new; the new SSI-soundness suite + contention suite green at `-race -count=5`,
no hang). Blind-write throughput unchanged (~50k durable-writes/s — the adapter never taxes the
engine blind fast path). NO on-disk format change (the comparer / `skydb.mvcc.v1`, the commit +
durability path, and `encodeIndexKey` are untouched).

### What shipped (Go only — provable with Go tests)

1. **Multi-collection `Txn` engine change (§2.1)** — `txn.go`. A `Txn` now carries an optional
   per-change `collResolver func(userKey []byte) CollID` (installed via `SetCollResolver`);
   `buildReq` stamps each emitted `KeyChange.Coll` via `collOf(uk)` — the resolver's per-change
   attribution from the `collName ‖ 0x1F ‖ pk` userKey prefix, else the single `SetCollection`
   fallback. The single-collection Phase-2 behaviour is preserved (resolver nil ⇒ `tx.coll`), so
   all 64 prior tests stay green. The installed indexer is likewise ONE multi-collection closure
   that parses `collName` from the userKey (its signature is unchanged — the userKey already
   namespaces the collection). NO key-format change.
2. **Go value types + `Backend` interface (§1.2)** — `backend.go`. `CollSchema` / `ColSpec` /
   `IndexSpec` / `ColValue` / `QueryPlan` / `OrderSpec`, the `Backend` interface (Get / Put /
   Insert / Delete / Query / Count / Transaction / SelectRaw / Capabilities / Close), the
   `TxHandle` txn surface, the separate `CrossInstanceReactive` interface (+ `Subscription` /
   `Change` seam), `Capabilities`, and the data-key + reserved-unique-keyspace layout
   (`0x1F` data separator, `0x1E` unique-key tag).
3. **Embedded adapter (`*EmbeddedBackend`, §3.3)** — `embedded.go`. Implements `Backend` +
   `CrossInstanceReactive` over the Phase-1/2 `Engine`: blind CRUD writes + snapshot reads,
   Query/Count as PK-ordered scan + in-RAM `bluedbEvalCond`, Transaction over `Engine.Transact`
   with the read-set contract, generated-field fill (serial PK + defaultNow), a per-collection
   schema registry driving the resolver + indexer.
4. **Codec-driven indexer `buildIndexer(CollSchema)` (§2.2)** — `indexer.go`. Emits one
   `IndexCoord` per declared single-column-ascending index via the SAME `encodeIndexKey` the
   scan-bound builder uses (byte-match by construction — R-2.1 at the L3 boundary; asserted by
   the encode-identity property test). NULL/absent emits no coord (§2.3). Record↔column mapping
   is the codec JSON blob (`decodeColumns`) with the value constructors
   (`IntVal`/`TextVal`/`BoolVal`/`RealVal`/`MoneyVal`/`BlobVal`) producing byte-identical
   normalization.
5. **txn-`Query` read-set contract (§2.6)** — `embeddedTx.Query` + `classifyIndexable` (`cond.go`).
   A single indexable range/eq leaf on a declared range-optimized index → `Txn.ScanRange`
   (precise index-range read-set) + residual in-RAM filter; anything else → `Txn.ScanCollection`
   (records the collection witness AND materializes — never a bare `reader.Iterate` that records
   nothing).
6. **IS-NULL + not-orderable → fallback witness (§2.3)** — an `isNull`/`notNull` leaf, and a
   predicate on a not-order-preserving `ColType` (Money/Decimal/blob/real — passed as an explicit
   not-orderable engine `ColType` in the `CollSchema`), route to `WitnessCollection`, never an
   index byte-range. A not-orderable column NEVER yields a range coord for validation.
7. **`unique` via SSI (§2.7)** — `txWrite`/`txDelete` read-then-write a reserved stored
   unique-index point key `collName ‖ 0x1E ‖ indexName ‖ 0x1F ‖ encodeIndexKey(value) → pk`. Two
   concurrent inserts of the same value conflict at validation (the loser's point read of the
   unique key hits the winner's write); the loser's retry re-reads the now-present key and returns
   a deterministic `ErrUniqueViolation`.
8. **Go SSI-soundness conformance suite** — `phase3a_ssi_test.go` + `phase3a_test.go`:
   `TxnQueryPhantomRejected`, `MultiCollectionStamping`, `NotOrderableMoneyFallback`,
   `IsNullFallback`, `UniqueViaSSI`, `RangeOptimizedPrecise`, `IndexerEncodeIdentity`,
   `IndexerNullEmitsNoCoord`, `CRUDRoundTrip`, `QueryOrderingAndPaging`, `BlindPutStaysOnFastPath`,
   `CapabilitiesAndSeams`.

### Deferred with the adapter (documented `// TODO(phase3b/c)` seams)

- **General stored secondary-index seek keys** — Phase 3a Query is PK-scan + in-RAM eval + the
  read-set contract (correct + parity-provable). The O(log n + k) seek fast-path over stored
  secondary-index keys is Phase 3b/4. (The STORED unique-index point keys of §2.7 are NOT
  deferred — they ship in 3a.)
- **Durable serial across restart** — the generated-PK sequence is an in-process counter in 3a;
  a durable engine sequence is a Phase-3b refinement.
- **`CrossInstanceReactive.Watch` commit-path evaluation** — the seam ships in 3a
  (`EmbeddedBackend.Watch` returns `ErrReactiveSeamPhase4`); Phase 4 wires the commit-path
  evaluation of the resolved plan.
- **Composite + descending indexes** — out of v1 scope (§2.5): L0's `index : String` is
  single-column ascending only; they need NEW L0 builders. The engine's `encodeCompositeKey` +
  `Descending` flag exist but are not reachable from L0 in v1.

## Phase 3b — Sky `Std.Persist` port + `Std.Codec` non-order-preserving MARKER — pending

- Port the pure-Sky `case conn of` dispatch + universal verbs; swap the `KvConn` payload to the
  new embedded handle; rebuild the `Embedded_coll*` KV-arm kernels over the `Backend` (§3.2/§3.3).
- The `Std.Codec` non-order-preserving marker that DERIVES the not-orderable `ColType` a Phase-3a
  `CollSchema` currently takes explicitly (§2.3): a `Codec.map`/non-primitive-backed/Money/Decimal
  column routes to the fallback engine `ColType`, and `colTypeFor`'s `Nothing`/`_` default routes
  UNRESOLVED fields to the fallback too.
- `CollSchema` derivation + memoization from the KV-arm kernel args (`Store.colsOf` +
  `indexFieldValues`/`indexFieldTypes`, ADAPTED to carry `ColType`).
- Does NOT touch the Sky stdlib or the compiler in 3a — this is the 3b surface.

## Phase 3c — SQL adapters + dialect-aware renderer + parity + capability check — pending

- The SQL arm of the ported `case conn of` over `Std.Db`/`Store` (Design B — no new Go SQL
  `Backend`); the dialect-aware `renderCond`/`orderTail` ADAPT (forced `NULLS FIRST` + forced
  `LIKE` collation) with the matching `bluedbEvalCond`; the SQL≡KV parity gate for the documented
  forced-semantics subset (`examples/55-persist-query`); the cross-instance capability check (§5 —
  single-instance watch is NEVER gated).
