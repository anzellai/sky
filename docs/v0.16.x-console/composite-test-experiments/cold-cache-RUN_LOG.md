# Cold-cache cabal-test baseline — Experiment #1

Each row: a single run on a freshly-wiped go-build cache.
Budget: wall < 600 s, peak cache < 5 GB, peak RSS < 4 GB.

| Date (UTC) | sky commit | CPU | RAM | Wall (s) | Peak cache (GB) | Peak RSS (GB) | Disk Δ (GB) | Examples | Status |
|---|---|---|---|---|---|---|---|---|---|
| 2026-06-04 08:29 | 24c28567 | Apple M1 | 16 GB | 2222 | 81.26 | 2.66 | 5.46 | 502 examples, 0 failures, 1 pending | ✓ wall>10min cache>5GB |
| 2026-06-04 10:15 | 92626eb2 | Apple M1 | 16 GB | 2183 | 81.22 | 2.66 | -10.29 | 501 examples, 0 failures, 1 pending | ✓ wall>10min cache>5GB |
