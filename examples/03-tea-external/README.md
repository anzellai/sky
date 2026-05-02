# TEA with External Packages

Demonstrates the TEA pattern (model / update / view / subscriptions) using external Go packages (`github.com/google/uuid` and `github.com/joho/godotenv`).

## Build & Run

```bash
sky install
sky build src/Main.sky
./sky-out/app
```

Requires `sky install` first to fetch Go dependencies and generate FFI bindings.

<img width="830" height="227" alt="03-tea-external" src="https://github.com/user-attachments/assets/66871b17-a026-4e24-92b2-6a483729d931" />
