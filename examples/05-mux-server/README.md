# Mux Server

HTTP server with gorilla/mux routing. Defines routes for `/`, `/echo`, and `/ping`.

## Build & Run

```bash
sky install
sky build src/Main.sky
./sky-out/app
```

Requires `sky install` first to fetch Go dependencies. The server listens on port 4000.
