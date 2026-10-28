# Persistent B+-tree prototype for `Dict` (design 1b, stage D2) — 2026-10-28

**Question.** Stage D2 replaces `Dict k v` (`map[string]V`, copied on every
insert: O(n)) with a persistent B+-tree behind a phantom-typed handle. The
review set a gate before the migration starts: **400 ns or less per insert at
1e5 keys**, trying leaf widths 8, 16 and 32.

**Verdict: the gate is NOT met.** The best variant measured 504 ns per random
insert at 1e5 keys under the Sky runtime's `GOGC=400`, and 622 ns under Go's
default `GOGC=100`. The D2 migration was therefore not started, and the
`dict-counts` program of `xtask perf-gate` stays red (it is not weakened).

Every number below is a line of [`bench.txt`](bench.txt), produced by
`go test -run XXX -bench . -benchmem -count=1 .` in this directory, once with
the default `GOGC` and once with `GOGC=400`, on an Apple M-series laptop with
Go 1.26.1. One run each: treat differences under about 10% as noise.

| Variant | Layout | insert, 1e5 random keys (ns) GOGC 100 / 400 | 1e6 get+insert, 1e5 keys (ns per update) GOGC 100 / 400 |
|---|---|---|---|
| v1 `bt.go`, W8 | erased `any` keys + values, comparator func, exact-length slices, 3 allocs per node | 950 / 757 | 820 / 664 |
| v1, W16 | | 843 / 682 | 666 / 543 |
| v1, W32 | | 942 / 691 | 628 / 475 |
| v2 `bt2.go`, W8 | fixed `[W]any` arrays, one alloc per node | 866 / 631 | 928 / 625 |
| v3 `bt3.go`, W8 | keys typed `K` (here `int`, pointer-free), values `any` | 699 / 590 | 641 / 521 |
| **v3, W16** | | **622 / 504** | 528 / 421 |
| v3, W32 | | 678 / 517 | **485 / 364** |
| v4 `bt4.go`, W16 | (key, value) pair entries | 760 / 559 | 747 / 533 |

What the numbers say:

* **Allocation, not comparison, is the cost.** Each insert copies one
  root-to-leaf path. At 1e5 keys that is 4 to 5 levels, about 1 kB and 10 to
  12 allocations per insert (`-benchmem`, `bench.txt`), most of it pointer-ful
  memory the collector must scan. A typed, pointer-free key array (v3) is the
  only layout that helped.
* **The perf-gate workload itself would pass with v3.** `dict-counts` does
  1e6 updates over 1e5 keys: v3 W32 measured 485 ns per update (0.49 s in all)
  at `GOGC=100`, under the gate's 3 s ceiling and the request's 1 s. The
  review's per-insert gate on random keys is the stricter bar, and it is the
  one the migration was made conditional on.
* **v3 is the int-key best case.** A real `Dict` also needs String, Float,
  Char and composite (tuple) keys. A typed key array per key kind, plus an
  erased comparator for tuples, adds a dispatch per operation; the erased-key
  layout (v1) is 680 to 950 ns.

What would change the verdict (not done here): a transient builder for
`Dict.fromList` / `foldl`-into-a-dict shapes that the compiler can prove
linear, or an arena for path copies. Neither fits the review's constraints
for this release.

Files: `bt*.go` (the four variants), `bt*_test.go` (a model test against a Go
map for each variant, including old versions staying unchanged, and the
benchmarks), `bench.txt` (the raw output).
