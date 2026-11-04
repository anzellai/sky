# Step-cost measurement

`scripts/spa-step-cost-e2e.sh` builds the public `spa-step-cost` fixture with a
fresh compiler and runs Chromium, WebKit and Firefox.
`scripts/spa-step-cost-mixed-e2e.sh` runs the same journey with a payload ADT
read by the view and a nested Editor whose brand form changes while its
product list remains unchanged, exercising prepared recursive persistence. Missing tools or browser
engines fail. The fixture separates retained data size from visible row count.
It checks ignored keys, normal input, asynchronous work, restore, handler payload
freshness, synthetic identity changes and four viewport widths.

The harness enables internal numeric profiling before wasm boot. Ordinary apps
do not enable it. No model contents, session values or environment data appear
in the profile. This is diagnostic plumbing, not an application API or a basis
for changing runtime decisions.

| Counter or duration | What it measures |
|---|---|
| steps | Executed TEA steps |
| update | Guard and update evaluation |
| view | User view evaluation |
| vnode | HTML-to-VNode conversion |
| style | Node path assignment and style injection |
| patch | Render planning, DOM application, deferred-input handling and handler refresh |
| post | URL reconciliation, Cmd interpretation and subscription reconciliation |
| preparedEncodes, preparedFallbacks | Strict codec preparation invocations and already-prepared Full-equivalent fallbacks |
| fieldMarshals, closedFieldReuses | Mixed-field marshals and proven closed-field/recursive atomic-leaf reuse |
| fieldEncodes, fieldEncodedBytes | Field conversions and marshaled field bytes, including unknown fields on every logical invocation |
| materializedBytes, bridgeBytes | Whole selected payload bytes joined and bytes copied into browser strings |
| modelEncodes, encodedBytes | Actual full model encoder calls and output bytes |
| encodeNanos | Encoding decisions, bounded identity checks and full encoder work |
| modelEncodeReuses, encodeComparedNodes | Successful next-encoding reuse and bounded witness visits |
| writeNanos | Entire persistence write pipeline, including exclusions, identity processing and storage |
| sessionNanos | Protected projections and session-clear decision after encoding |
| storageWrites | Browser storage calls intercepted by the synthetic harness |

The aggregate patch and write durations must not be described as pure DOM or
disk costs. Diagnostic export and profiling overhead are outside these phase
durations. End-to-end measurements with profiling disabled are also required
before claiming a latency improvement. Chromium CPU throttling is supported by
the verifier's `--cpu 4`; actual mobile measurements remain a separate gate.

Saved JSON reports contain numeric measurements and synthetic scenario names.
They are evidence for the tested source, engine and workload only. Neither a
green report nor an unchanged-step speedup proves arbitrary programme scaling.
