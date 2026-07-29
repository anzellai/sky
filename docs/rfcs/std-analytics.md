# RFC: `Std.Analytics` — typed product analytics for Sky

**Status:** In progress (`feat/std-analytics`) · **Milestone:** v1 · **Author:** design pass (grilled)

### Progress (implemented on the branch)

- ✅ **Capture core** — open typed-payload builder (`event` + `string`/`int`/
  `float`/`bool`/`money`/`pii`), load-bearing `Pii` redaction.
- ✅ **`trackEvent` derive** — reflective payload from the app's own typed union
  (ctor → snake_case name, record fields → typed props); handles both SkyADT +
  sealed-iface representations via `unwrapADTShape`.
- ✅ **Consent + identity backbone** — `Consent` (Anonymous default / Granted /
  Denied), `identify` (explicit, never automatic), anonymous-by-default,
  session-scoped via the goroutine-local session stamp (multi-session isolation
  proven).
- ✅ **Auto page-views** — opt-in `analytics = { pageViews = True }` on Live.app;
  consent-gated, from the single `handleInitial` funnel.
- ✅ **Context + IP-anon** — device (User-Agent) + IP truncated (v4 last octet,
  v6 last 80 bits) before storage.
- ✅ **Pluggable sinks** — `Sink = StderrSink | FileSink | Custom fn` +
  `configure`; `Custom` defers all provider choice to userland.
- ⏳ **Remaining** (need product/infra decisions — §15): per-sink Pii clearance,
  first-class provider wrappers, the console dashboard + store, `erase`, the
  opt-in Level-2 encoder, docs/example + template sync.

---

## 1. Summary

A batteries-included, **typed** product-analytics module: capture page views,
actions, and e-commerce events with rich metadata (user, device, product,
price), enrich them with context automatically, gate them on consent, and route
them to a local store (rendered in the console) and/or an external provider or
warehouse.

The differentiator is not the dashboard — it's that **your analytics schema is
Sky-typed and checked at compile time**, and that **privacy is load-bearing in
the type system**, not a bolt-on. Every other SDK on the market is stringly
typed and consent is an afterthought.

> **Scope, stated honestly up front (this changed under review — see §14).**
> The stdlib owns **100% of capture, context, consent, and routing**. It does
> **not** own analysis. Deep questions — funnels at scale, retention curves,
> cohorts — are answered by **exporting** to a warehouse or provider. The
> built-in console view is a **verify/debug surface plus the common counts**,
> not a BI tool. The honest one-line promise:
> *"Typed capture + auto-context + consent + routing, a live console view and
> the common counts; bring a warehouse/provider for funnels-at-scale, retention
> and cohorts."*

---

## 2. Motivation

### 2.1 The gap
Sky already has **observability**: `Std.Trace` (spans), `Std.Log`, `/_sky/metrics`,
the console, `sky run --profile`. That answers *"is my system healthy, why is
this request slow?"* for **operators**.

It has **nothing** for **product analytics**: *"what are my users doing, what
converts?"* for **product/business** people. These look alike (both are "events")
but differ in consumer, retention, identity model, and query shape (point-lookup
o11y vs OLAP analytics). Overloading one onto the other is the classic mistake;
analytics is a **separate typed channel**.

### 2.2 Why it belongs in Sky
- Sky targets exactly the SaaS/commerce apps (Sky.Live, the shop examples,
  `Std.Money`) that all need this. It fits the "everything a startup needs" set
  next to `Std.Auth` / `Std.Db` / `Std.Email` / `Std.Money`.
- Most of the plumbing already exists: the console app + SQLite hot-store + OTLP
  hub + provider-ADT pattern (`Std.Email`).
- **Strategic:** a SkyDeploy tenant gets typed product analytics + a live view
  **for free**, data staying in their own tenant (privacy) — no Segment/Amplitude
  bill. Same GTM wedge as the console strategy.

---

## 3. Goals / Non-goals

**Goals**
- Typed capture of the common event shapes (page view, action, identify,
  e-commerce) with compile-time-checked properties.
- Auto-context (device, route, session, timestamp) with **zero re-threading**.
- **Default-safe privacy**: anonymous by default, explicit identify,
  consent-gated, IP-anonymized (see §11 — this is a hard requirement).
  *(Shipped deviation, v0.19.1: consent defaults to `Granted` for DX — an app
  that enables analytics captures fully; privacy-conscious apps downgrade to
  `Anonymous`/`Denied` via `setConsent` behind a consent banner. IP anonymization
  + the opt-in identity model are unchanged.)*
- Pluggable sinks (local + external providers/warehouses) via one ADT.
- A live/verify console view + the common aggregates.
- Progressive disclosure: useful with zero config; every layer optional.

**Non-goals (explicit disclaimers for the docs)**
- Not a warehouse. The local store has a bounded capacity envelope (§13).
- Not a BI/dashboard product. Funnels-at-scale, retention curves, cohort
  retention, cross-device identity stitching → **export**.
- No general stdlib query DSL. Arbitrary business questions → `Std.Db` or the
  exported warehouse.
- Not "100% of analytics in the stdlib." 100% of **capture**; a bounded slice of
  **analysis**.

---

## 4. Architecture at a glance

```
  view/update ──► Analytics.track ──► [consent gate] ──► [context enrich]
                                                              │
                                            ┌─────────────────┴─────────────────┐
                                            ▼                                    ▼
                                    local sink (SQLite)                 export sink(s)
                                      │        │                     (PostHog/Segment/GA4/
                                      │        │                      warehouse; PII-scoped)
                              console "Analytics"   common typed
                              live + counts view    aggregate helpers
```

One `track`, one enrich+consent boundary, fan-out to N sinks. Auto page-views and
session events enter the *same* pipe as custom events, so everything is uniform
downstream.

---

## 5. The core, after the grill (this is the important part)

The initial design made the app's typed `Event` **union** the single source of
truth, with a mandatory `encode : Event -> Payload`. Two review lenses (DX and
extensibility) broke that. The corrected core:

### 5.1 Foundation: an OPEN typed-payload builder
The wire/foundation type is an **open** event built from **typed** property
builders — the Segment shape, but type-safe on values and PII:

```elm
Analytics.event : String -> List Prop -> Event

-- typed prop builders (like a JSON encoder):
Analytics.string  : String -> String -> Prop
Analytics.int     : String -> Int    -> Prop
Analytics.float   : String -> Float  -> Prop
Analytics.bool    : String -> Bool   -> Prop
Analytics.money   : String -> Money  -> Prop      -- serialises lossless "USD 19.99"
Analytics.pii     : String -> Pii    -> Prop      -- PII-typed; consent/region gated (§11)
```

Any code — app OR a reusable library — can emit without coupling to a central
union:
```elm
Analytics.track (Analytics.event "product_viewed"
    [ Analytics.string "id" (ProductId.toString p.id)
    , Analytics.money "price" p.price ])
```

**Why open, not a closed union (grill §15-B):** Sky has no typeclasses/HKT, so it
*cannot* enforce a closed schema across library boundaries anyway. A reusable
`DatePicker` or a `Std.Auth`-style library must be able to emit
`auth.login_succeeded` without being in the app's union. A closed app-owned union
makes that impossible; an open builder makes it trivial. Pretending the union is
"the source of truth" was false.

### 5.2 Opt-in sugar: app-owned typed unions
For an app's **own** events, closing the schema buys real refactoring safety
(exhaustiveness, rename-propagation). So the union is offered as **opt-in sugar**
on top, not the foundation:

```elm
type Event                                    -- YOUR app's events, closed & typed
    = PageViewed { path : String }
    | ProductViewed { id : ProductId, price : Money }
    | Purchased { orderId : String, total : Money }

Analytics.trackEvent ProductViewed { id = p.id, price = p.price }
```

`trackEvent` turns a typed value into an `Analytics.Event`. How the value becomes
props is where the **default+custom blend** lives (§6).

---

## 6. The default + custom blend (your explicit concern)

The tension: **auto-derive** (convenient, but opaque/magic) vs **explicit
encoder** (safe/clear, but boilerplate). The resolution is *progressive
disclosure with a reflective default that PII-typing keeps safe.*

### 6.1 Three levels, each optional
**Level 0 — zero config.** Nothing written. You get:
- automatic page-view + session-start events (Sky.Live already owns routing),
- a default **local** sink (console SQLite),
- `Analytics.track (Analytics.event "signed_up" [...])` works immediately.

**Level 1 — typed events, derived payload (no encoder).** Define your `Event`
union and call `trackEvent`. With **no encoder written**, the runtime derives the
payload reflectively (the same record reflection `Db.query` already uses):
constructor `ProductViewed` → `"product_viewed"`; record fields → props
(`Money` → `"USD 19.99"`, `Int` → number, …).
- This removes the **boilerplate cliff** the DX lens flagged: adding a custom
  event is **one place** (a union variant), not three.
- **It is safe** (PII lens): the reflector only emits fields you typed. It never
  guesses that a bare `String` is an email. Identity/PII enters **only** through
  the explicit `Pii` type (§11), so auto-derive can't leak PII by accident.

**Level 2 — explicit encoder, opt-in for control.** When you need to rename a
prop, redact, reshape, or split, provide `encode : Event -> Analytics.Event` and
the compiler forces every variant to be handled:
```elm
encode e =
    case e of
        ProductViewed p ->
            Analytics.event "product_viewed"
                [ Analytics.string "sku" (ProductId.toString p.id)   -- renamed
                , Analytics.money "price" p.price ]
        ...
```
Override per-app; you never *owe* the whole encoder just to add one event.

### 6.2 Auto-events are lifted into the SAME pipe, visibly
Automatic events aren't a separate hidden stream. They're configured on the
`Live.app` cfg (row-open, like `head`/`consoleAuth`):
```elm
analytics =
    { context = \model req -> Analytics.context [ ... global props ... ]
    , onPageView = \pc -> Just (PageViewed { path = pc.path })   -- default provided
    , consent = \model -> model.consent                          -- §11
    , sinks = [ Analytics.consoleSink, Analytics.posthog cfg ]
    }
```
- **Override** the shape (`onPageView` returns your own event),
- **disable** a route (`onPageView` returns `Nothing`),
- **turn all auto-tracking off** (omit `onPageView`).

### 6.3 No magic — discoverability is first-class
The DX lens's sharpest point: auto-behaviour must be *seeable and flippable*.
- `SKY_ANALYTICS_DEBUG=1` logs every emitted event (name + props + which sinks +
  consent decision) to stderr.
- The console **Analytics → Live** tab streams events as they fire, so you watch
  exactly what's captured.
- Every automatic behaviour is one named field on the `analytics` cfg — reading
  the cfg tells you everything that fires. Nothing happens that isn't a field you
  can see and change.

**Net DX:** zero-config gives useful data; the first custom event is one union
variant; the encoder is opt-in; auto-events are visible cfg fields; a debug flag
and a live view remove the magic. That is the "default + custom blend, dev-
friendly and changeable" target.

---

## 7. Worked example (end to end)

```elm
import Std.Analytics as Analytics exposing (Pii)

type Event
    = ProductViewed { id : ProductId, price : Money }
    | Purchased { orderId : String, total : Money, items : Int }

-- update: emit a custom event (no encoder needed — derived)
AddToCartClicked id ->
    ( model, Analytics.trackEvent (ProductViewed { id = id, price = priceOf id }) )

-- on login: explicit identify (NEVER automatic — §11)
SignedIn user ->
    ( { model | session = Just user }
    , Analytics.identify user.id [ Analytics.pii "email" (Pii.email user.email) ] )

-- app wiring
main =
    Live.app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [...]
        , notFound = HomePage
        , analytics =
            { context = \m _ -> Analytics.context [ Analytics.string "plan" (planOf m) ]
            , onPageView = \pc -> Just (Analytics.event "page_viewed" [ Analytics.string "path" pc.path ])
            , consent = \m -> m.consent
            , sinks = [ Analytics.consoleSink, Analytics.posthog { key = env "POSTHOG_KEY", host = "eu" } ]
            }
        }
```

---

## 8. Identity & uniqueness (correctness the naive design gets wrong)

Unique-user and funnel metrics are **wrong** without an identity model. Explicit:
- Every session gets a stable **anonymous id** (cookie-backed). All pre-identify
  events carry it.
- `Analytics.identify : UserId -> List Prop -> Task Error ()` **stitches**
  anon → known from that point; the local store rewrites/links prior anon events
  in the same session to the known id.
- Unique counts are computed on the stitched id.
- **Explicit non-goal:** cross-device / cross-session-anon stitching is NOT done
  locally (it needs a real identity backend) — documented, so unique counts are
  honest ("unique within a browser/session unless identified").

---

## 9. Sinks (the `Std.Email` precedent)

One ADT, like `EmailProvider`:
```elm
type Sink
    = ConsoleSink                         -- local SQLite, renders in console
    | PostHog PostHogCfg
    | Segment SegmentCfg
    | GA4 GA4Cfg
    | Warehouse WarehouseCfg              -- BigQuery/ClickHouse/Postgres via Std.Db
    | Custom (Event -> Task Error ())     -- escape hatch
```
- **Local + export coexist** (grill §15-C): `sinks` is a list; you dual-write to
  `ConsoleSink` (keeps the console view alive) **and** an export. Routing to an
  external tool does not blind the console.
- Each sink declares a **PII/consent clearance** (§11); the pipeline redacts
  `Pii` props for sinks not cleared for the current consent/region.
- Batching/spooling reuses the console `HubExporter` spool infra.

---

## 10. Storage, query, console (honest capacity)

- **Local store:** SQLite, WAL, bounded retention + row caps (reuse the console
  hot-store). Honest envelope in the docs: *fine for dev + low-traffic + verify;*
  a stated ceiling (events/day × retention) beyond which you **must** export.
  It is explicitly a **verify/debug + common-counts** surface, not a warehouse.
- **Console "Analytics" tab:** (a) **Live** — real-time event stream (the "no
  magic" view); (b) **Counts** — top events, page-views over time, DAU/MAU,
  simple 3-step funnel, revenue (via `Money`), all over the local store within
  the envelope.
- **Common typed aggregate helpers** (over the local store): `countByEvent`,
  `countByDay`, `uniqueUsers`, `funnel [step,step,step]`. Small, typed, honest.
- **Everything else → export.** No general query DSL in stdlib; arbitrary
  questions use `Std.Db` on the exported table or a BI tool on the warehouse.

---

## 11. Consent & PII — **default posture INVERTED under review (hard requirement)**

The initial "auto-attach user id/email + zero-config auto page-views, default
privacy-preserving" is self-contradictory and, in the EU, unlawful pre-consent
PII processing. Required posture:

- **Anonymous by default.** Zero-config capture attaches **no PII** — only the
  anon id + non-identifying context (route, device class, timestamp). IP is
  **truncated/anonymized by default**.
- **Identity is explicit, never automatic.** `identify` is the *only* way user
  id/email attaches. Auto-attaching Auth's known email is a footgun and is not
  done.
- **Consent-gated.** `consent : Model -> Consent` on the cfg. Before consent:
  configurable **anonymous-only** (default) or **drop**. On consent: identity +
  export enabled; buffered anon events flush. Consent state lives in the session
  (Sky owns sessions), so the gate sits at the enrich boundary, not bolted on.
- **Typed PII is load-bearing, not decorative.** PII may enter props *only* via
  `Analytics.pii "email" (Pii.email …)`, producing a `Pii`-typed value. Each sink
  has a clearance; the pipeline **strips or hashes `Pii` props by type** for any
  sink/region not cleared. A bare `String` prop cannot carry PII through the
  cleared path, and auto-derive (§6.1) never emits raw strings as PII. (Honest
  limit: a dev *can* still stuff an email into a plain `string` prop — the type
  makes the *safe* path the *easy* path and blocks the derive/identify path from
  leaking; it is not a total sandbox.)
- **Export controls:** consent-scoped (analytics vs marketing), **region pinning**
  (EU host = data stays), **right-to-erasure** (`Analytics.erase : AnonId | UserId
  -> Task Error ()` deletes locally; export erasure delegated to the provider).

**Ships ON by default:** anonymous capture, IP anonymization, consent gate in
"anonymous-until-consent" mode, local ConsoleSink.
**Ships OFF by default:** any PII attachment, any external export, cross-session
identity.

---

## 12. Extensibility: how libraries emit events (grill §15-B)

Because the core is the **open builder**, a reusable package emits directly:
```elm
-- inside a library, no coupling to the app's Event union:
Analytics.track (Analytics.event "auth.login_succeeded"
    [ Analytics.string "method" "password" ])
```
Convention: libraries **namespace** event names (`auth.*`, `datepicker.*`). The
app's closed union covers the app's own events; library events flow through the
same pipe, same context, same consent, same sinks — uniform. This is the analytics
analogue of the `toMsg`/`Config` outcome: emit into the shared space directly
rather than mapping after the fact.

---

## 13. Honest coverage table (capture vs local-aggregate vs export)

| Product ask | Capture | Local console/aggregate | Needs export |
|---|---|---|---|
| Page views / event counts | ✅ | ✅ | — |
| DAU / MAU | ✅ | ✅ (stitched id) | — |
| Event-property filtering | ✅ | ✅ (bounded) | at scale |
| Session duration | ✅ | ✅ | — |
| Revenue / user (Money) | ✅ | ✅ | — |
| Simple funnel (≤ few steps) | ✅ | ✅ (bounded) | at scale |
| UTM / attribution | ✅ (capture) | partial | ✅ (analysis) |
| A/B exposure | ✅ (capture) | partial | ✅ (analysis) |
| Segment breakdowns | ✅ | partial | ✅ |
| Retention curve / cohort retention | ✅ (capture) | ❌ | ✅ |
| Cross-device unique users | ⚠️ (identified only) | ❌ | ✅ |

**Reading:** capture coverage ≈ 100%; local *analysis* ≈ 50–60% of common asks
within the envelope; deep/at-scale analysis is an **export** story. The docs must
say this plainly so nobody is surprised.

---

## 14. Grill: tensions & resolutions (what the review changed)

| Lens | Sharpest failure | Resolution (design change) |
|---|---|---|
| **DX / blend** | Adding one event touching union+encoder+cfg = 3 places vs a one-liner; boilerplate cliff at the first custom event | **Reflective derive by default → encoder is opt-in.** One event = one union variant. Debug flag + live console view kill the "magic." (§6) |
| **Type system / extensibility** | No typeclasses → a giant app union + one exhaustive encoder is a monolith; **libraries can't emit into a closed app union** | **Core is an OPEN typed-payload builder; app unions are opt-in sugar.** Libraries emit directly, namespaced. (§5, §12) |
| **Scope / storage** | SQLite hot-store misleads as a "warehouse"; unique/funnel metrics silently wrong without identity | **Explicit capacity envelope + identity/stitching model + honest coverage table.** Console = verify/counts, not BI. (§8, §10, §13) |
| **Privacy / consent** | Zero-config auto page-views + auto user email = unlawful pre-consent PII; "typed PII" was decorative | **Defaults INVERTED: anonymous-by-default, explicit identify, consent gate, IP-anon; `Pii` type made load-bearing (sink clearance strips by type).** (§11) |

The grill materially improved the core (open builder, optional encoder, inverted
privacy defaults). Recording it so the shape isn't re-litigated.

---

## 15. Open decisions (for you)

1. **Module name:** `Std.Analytics` (proposed) vs `Std.Track` / `Std.Insights`.
2. **Derive vs encoder default:** ship reflective derive as the default (max DX),
   or require the encoder (max explicitness)? Recommendation: derive default,
   encoder opt-in — but it leans on runtime reflection (a small, bounded use like
   `Db.query`).
3. **Console dashboard depth:** ship Live + Counts in v1, or Live-only (pure
   verify) with all analysis via export? Recommendation: Live + a *small* Counts
   set, clearly bounded.
4. **First-class e-commerce helpers** (`productViewed`/`purchased` with `Money`)
   given the shop focus — yes/no for v1.
5. **Consent model surface:** a Sky-owned `Consent` type + a ready-made banner
   component, or just the gate hook and BYO banner?

---

## 16. Suggested phasing (v1)

- **P1 — capture core:** open builder + typed props (incl. `Money`, `Pii`),
  `track`/`trackEvent`, reflective derive, auto page-view/session, context
  enrich, `ConsoleSink`, `SKY_ANALYTICS_DEBUG`. Default-safe (anonymous).
- **P2 — consent & PII:** consent gate, `identify`, `Pii` clearance/redaction,
  IP-anon, `erase`.
- **P3 — export sinks:** provider ADT (PostHog/Segment/GA4/Warehouse), spool,
  region pinning, consent-scoped export.
- **P4 — console tab:** Live stream + the common aggregate helpers/Counts view.
- **P5 — docs/examples:** honest scope docs, an e-commerce example wired to the
  console, template/CLAUDE.md sync.
