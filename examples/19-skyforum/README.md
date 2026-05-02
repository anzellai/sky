# SkyForum

A Reddit/HackerNews-style demo built with Sky.Live and Std.Ui. The view layer is split across multiple modules (State / Update / View.Common / View.Posts / View.Detail / View.Compose / View.Login) so each per-module type-check stays well below the heap-exhaustion limit (CLAUDE.md Limitation #17).
A single monolithic Main with the same surface OOMs the type-checker and locked the Mac that birthed scripts/mem-guard.sh — the split form gives us the full feature surface at zero ergonomics cost pending compiler fix.

## Build & Run

```bash
sky build src/Main.sky
./sky-out/app
```

Open `http://localhost:8000` in your browser.
