# TEA with External Packages

Demonstrates The Elm Architecture (TEA) pattern using external Go packages (`github.com/google/uuid` and `github.com/joho/godotenv`).

## Build & Run

```bash
sky install
sky build src/Main.sky
./sky-out/app
```

Requires `sky install` first to fetch Go dependencies and generate FFI bindings.
