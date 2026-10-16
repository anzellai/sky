# Griller: effectiveness (2026-10-15)
Verdict: plan misses the dominant cause. The Sky.Spa split emits wire code that grows with (server arms x Model fields).
- build_wire copies every Model field into a per-arm request record when the arm reads the whole model (spa_split.rs:2324-2326), and a response record when it writes the whole model (:2350-2351). Condition request_whole_model() (spa_partition.rs:1103-1105), set by compute_branch_io when model goes to an opaque helper or the model param is not found (spa_partition.rs:5050-5054).
- Each such arm: own `type alias XReq` + Codec.object |> Codec.field chain of F fields, in shared/, frontend/src/, backend/src/Shared.sky. Go structs already dedup by shape; Sky source and codecs do not.
MEASURED (copy of 62-app-notes, N groups = 2 fields + 3 arms each):
- one-field arms: N=100 split 0.81 s / 31.5k lines; N=400 4.8 s / 119k; backend leg 13.2 s at N=100.
- whole-model arms: N=50: 1,601 user lines -> 49k generated; typecheck 4.3 s; lower+emit 5.6 s; main.go 1.59 MB; go build 10.2 s -p 3; leg 20.3 s; 1.36 GB.
  N=100: 2,651 -> 156k; typecheck 30.8 s; lower+emit 36.4 s; main.go 5.2 MB; go build 21.8 s at -p 1; leg 89.2 s; 4.75 GB.
  Typecheck/lower grow ~7x per doubling of N (near cubic). Codecs are 53% of main.go at N=50. Frontend imports the same Shared.sky (paid twice).
INFERRED N=200: ~200 s typecheck + 250 s lowering per leg x 2 legs, main.go ~17 MB, Go estimate ~12 GB -> swap on 16 GB: 25-38 min class on any core count.
Commit cea3cd983: a 22k-line Spa app's main compiled in 6.7 s, so Go of a large main alone does not explain 25 min.
Phase 2/3b do not reach it (cost concentrated in one module Shared + Main_update).
Recommended: Phase 1.0 one ModelWire alias+codec for whole-model arms (like BOXED_MODEL_FIELD spa_partition.rs:1666-1695, emit_build_req/emit_reconstruct/emit_apply_delta spa_split.rs:2098-2289), dedupe req/resp records by sorted field signature; same wire bytes (spa_diff_harness, split flow tests); generated lines O(arms+fields).
Investigate codec pipeline cost: per-codec cost ~1.8x for 2x fields; possible quadratic unification/lowering of long |> chains in F. Test with a 300-field record.
Generator must include 100-200 field Model, whole-model server arms, Cmd.perform chains, forms, Std.Db, auth, SSR routes.
Guard: xtask ratchet on generated/user lines ratio and leg typecheck time at N=50/100/200 (falsifier: revert ModelWire).
Re-check go_jobs after 1.0.
Scripts: grill/genarms.py, genwhole.py, runsplit.sh, runbe.sh.
