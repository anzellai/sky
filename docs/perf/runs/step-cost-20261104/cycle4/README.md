# Cycle 4 field persistence diagnostics

These six actual wasm browser journeys passed in reference/incremental modes.
Source identity and conditions are in `conditions.json`; work counts are in
`counts.json`. Profiling was enabled, one repeat per browser/mode, no CPU
throttling or physical-device validation. Timings are diagnostic only.

For twenty typing messages, the reference performs forty full model encodes.
The restricted incremental path performs zero full encodes and ten text-field
encodes (75 JSON bytes), at 1K, 10K and 100K retained entries. All update/view/post
messages still execute. Ten ignored keys perform zero field encodes, zero
payload materialization and zero wasm string copying while retaining all ten
storage writes at the two sizes below the cap.

For the 6 MB case, both paths retain the original oversized-snapshot policy:
no storage writes. The incremental path also avoids whole-payload
materialization. For changed snapshots below the cap, reconstruction and writes
still scale with stored bytes; these runs do not establish flat persistence or
view latency. View execution remains complete, including the 200-row case.

Reload, asynchronous command completion, handler payload changes, identity
transitions, viewport overflow and mid-string edits are asserted. The first
expanded reference run exposed an uncontrolled caret precondition; the test now
sets that position explicitly and separately checks a mid-string edit. Firefox's
reference path exceeded the default single-action timeout when sending all ten
oversized-model characters together. Ten single-character actions preserve the
same messages and assertions under unchanged browser/memory deadlines.

The wider FacePlan model still needs ADT/container certification and storage/view
work. No private-app performance improvement is claimed by this synthetic gate.
