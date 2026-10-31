# Persistent `Dict` (stage D2): design chosen by measurement — 2026-10-28

**Question.** `Dict.insert` / `remove` copied the whole Go map (`map[string]V`),
so a fold that builds a Dict was O(n²): perf-gate `dict-counts` took 1.2 s at
1e4 updates and 20 s at 4e4. D2 replaces the map with a persistent structure.
The gates, in the order the request set them:

* (a) `xtask perf-gate` `dict-counts` green, unchanged: growth `t(4n)/t(n)` at
  most 6, at most 8 allocations per element, 3 s ceiling.
* (b) at sizes 1, 4, 8, 16 and 64, insert and get no slower than 1.1x the old
  map-copy cost, measured first.
* (c) get stays O(log n) and fast at 1e6.

The B+-tree prototype (`../dict-btree-prototype-20261028/`) handled Int keys
only in its best layout. A Dict key is already an encoded string
(`encodeDictKey`), and every ordered operation already decodes and sorts the
entries (`dictEntries`), so a hash structure over the same strings needs no
key comparator at all and keeps key order exactly as before.

**Verdict: a hybrid.** A flat array of up to 16 entries (linear scan with a
last-byte prefilter), then a 16-way hash array mapped trie (HAMT) whose child
arrays are held by a bare pointer (length = popcount of the bitmap), so an
update allocates one slot array per level. Keys of up to 16 bytes take an
inline keyed hash (two 64x64->128 multiplies, seeded from `hash/maphash`);
longer keys use `maphash`. Shipped as `rt.SkyDict[V]`
(`runtime-go/rt/dict_persistent.go`). All three gates pass.

## 1. Choosing the layout (`*.go` in this directory)

`go test -run XXX -bench . -benchmem -benchtime=300ms -count=1 .` with
`GOGC=400` (the Sky runtime default), Apple M-series, Go 1.26.1, one run:
[`bench-gogc400.txt`](bench-gogc400.txt). Keys are encoded Int keys
(`"\x01i" + decimal`), values `int`, all behind one interface wrapper (the same
overhead for every variant).

| Variant | Layout | 1e5 random inserts (ns / insert) | dict-counts shape, 1e6 get+insert over 1e5 keys (ns / update, B, allocs per update) | get at 1e6 (ns) |
|---|---|---|---|---|
| `ptr32` | node = {bitmap, []slot}, slot = key, val, child pointer | 684 | 951, 3.6 kB, 9.0 | 61 |
| `inline32` | child (bitmap, []slot) inside the parent slot | 1013 | 1231, 6.2 kB, 5.0 | 58 |
| `inline16` | the same, 16-way | 713 | 922, 4.0 kB, 5.8 | 71 |
| `compact32` | child array by bare pointer, 40-byte slot | 677 | 837, 4.3 kB, 5.0 | 55 |
| **`compact16`** | **the same, 16-way (shipped)** | **549** | **716, 2.8 kB, 5.8** | **67** |
| `compact8` | the same, 8-way | 530 | 685, 1.8 kB, 7.3 | 84 |

* Bytes copied per update decide the time, as for the B+-tree: a wider node
  copies more per level. 8-way is slightly faster but costs 7.3 allocations
  per update, which leaves no room under gate (a)'s budget of 8 once the
  program's own allocations are added. 16-way is the balance.
* Small sizes ([`bench-gogc400.txt`](bench-gogc400.txt), `BenchmarkSmall*`):
  every trie insert beats the map copy (at 64 entries 154 ns against 1378 ns),
  but a trie get at 64 entries (18 ns) is slower than a Go map (10 ns), and a
  flat array beats the map below 16 entries (`BenchmarkSmallGetFlatPF`: 3.5 ns
  at 1, 8.5 ns at 16, 14.4 ns at 32). Hence the flat tier up to 16.

## 2. Gate (b): kernel cost before and after

The benchmark is `runtime-go/rt/dict_cost_bench_test.go`, in the exact shapes
the compiler emits; it ran on HEAD 9475000c2 (the map) and on the change:
[`kernel-before.txt`](kernel-before.txt), [`kernel-after.txt`](kernel-after.txt),
the table is [`ratio.txt`](ratio.txt) (minimum of 3 runs, ns per call).

| Size | insert Int | insert String | get Int (typed) | get String (typed) | get Int (erased) | get String (erased) |
|---|---|---|---|---|---|---|
| 1 | 589 -> 113 (0.19) | 447 -> 101 (0.23) | 93 -> 26 (0.27) | 14.9 -> 9.1 (0.61) | 121 -> 45 (0.37) | 32.9 -> 27.3 (0.83) |
| 4 | 825 -> 136 (0.17) | 681 -> 119 (0.17) | 105 -> 26 (0.25) | 15.4 -> 10.0 (0.65) | 125 -> 52 (0.41) | 33.8 -> 28.2 (0.84) |
| 8 | 1303 -> 156 (0.12) | 1169 -> 139 (0.12) | 107 -> 28 (0.26) | 16.1 -> 11.2 (0.70) | 128 -> 56 (0.44) | 35.0 -> 29.7 (0.85) |
| 16 | 2289 -> 868 (0.38) | 2314 -> 731 (0.32) | 97 -> 31 (0.32) | 20.0 -> 14.0 (0.70) | 122 -> 62 (0.50) | 37.6 -> 32.4 (0.86) |
| 64 | 7339 -> 243 (0.03) | 7482 -> 208 (0.03) | 96 -> 39 (0.41) | 18.4 -> 18.9 (1.02) | 122 -> 56 (0.46) | 37.1 -> 39.3 (1.06) |

Worst ratio 1.06 (an erased String-key get at 64 entries). Inserting the 17th
entry (the "16" column) builds the trie from the flat array in one pass.

What got the gets there, each measured on the way (the intermediate numbers are
not kept as files): the trie walk with locals instead of a slot copy (25 -> 14
ns at 64), the keyed short-key hash (maphash 5.0 ns -> 2.7 ns), and a direct
`SkyDict[any]` path in the erased kernels (no interface dispatch). Two changes
are shared with code outside the Dict and make the "after" column faster than
the structure alone: Int and Char keys are encoded without `fmt` (the bytes are
identical, `TestEncodeDictKeyFastPathsMatchTheGeneralForm`), and
`derefPointer` returns a non-pointer value without a reflect round trip.

## 3. Gate (c): get at 1e4 to 1e6

[`kernel-large-get.txt`](kernel-large-get.txt), `BenchmarkDictKernelGetLarge`,
random Int keys, `rt.Dict_getIntT`: 50 ns at 1e4, 69 ns at 1e5, 96 ns at 1e6,
0 allocations. The growth is the trie depth plus cache misses, O(log n).

## 4. Gate (a): perf-gate

`cargo run --release -q -p xtask -- perf-gate` (all six programs PASS):

    PASS dict-counts  t(n)=0.200s t(4n)=0.936s growth=4.69 mallocs/elem=7.819 (budget 8)

Before: 1.2 s at 1e4. Two compiler changes were needed besides the runtime,
both cited in the commit: `get` / `member` / `insert` / `remove` over a typed
Dict call typed twins with a typed key (`rt.Dict_getIntT`,
`rt.Dict_insertIntT`, …: no boxing of the key or the Dict, an Int key encoded
into a stack buffer), and a callback with a Dict parameter now takes the typed
eta path (it went through `reflect.Call` on every element, 11 allocations a
call).

## What stays

* Enumeration (`toList`, `keys`, `values`, `foldl`, `map`) sorts the decoded
  keys on every call, O(n log n), exactly as over the map.
* Composite keys stay `[E2008]`: the store keys on the same encoded strings,
  whose encoding of a composite key is not injective.
* The stored (gob) form of a Dict is new: a v0.28 Sky.Live session holding a
  Dict is dropped once on upgrade (`docs/migration/v0.29.md`).
