# Live Counter

Interactive counter using Sky.Live with server-sent events (SSE). Demonstrates the TEA pattern (model / update / view) running server-side with live UI updates.

## Build & Run

```bash
sky build src/Main.sky
./sky-out/app
```

The server listens on port 8000. Open `http://localhost:8000` in a browser.
