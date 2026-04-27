# TEA with External Packages

Demonstrates the TEA pattern (model / update / view / subscriptions) using external Go packages (`github.com/google/uuid` and `github.com/joho/godotenv`).

## Build & Run

```bash
sky install
sky build src/Main.sky
./sky-out/app
```

Requires `sky install` first to fetch Go dependencies and generate FFI bindings.
