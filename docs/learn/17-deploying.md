# Deploying

Sky compiles to a single static Go binary, so shipping is refreshingly boring.
What changes with scale isn't the code — it's the tier.

## Build the binary

```
sky build src/Main.sky
```

That produces `sky-out/app` — one self-contained executable, no runtime to
install on the server. Copy it to a machine and run it. For a Linux server from a
Mac, cross-compile the same way Go does (`GOOS=linux GOARCH=amd64`), then `scp` the
binary and run it under `systemd`. That's a complete deployment for a small app.

## The tier decides the architecture

Cast your mind back to the very start of the tour — the tier question. It comes
back here:

| Tier | Database | Sessions | Deployment |
|---|---|---|---|
| **Prototype / pet / internal** | SQLite (one file) | `memory` or `sqlite` | one binary on one VM |
| **Production** | PostgreSQL | shared store (`redis`/`postgres`) | multiple replicas behind a load balancer |

A pet project on one machine is *done* with SQLite and a memory session store.
Don't reach for Postgres, Redis, and Kubernetes to track your bookshelf.

## The production gate

When you deploy for real users, set `ENV=production`. That one switch:

- locks the dev console + banner off, and puts the metrics endpoint behind auth;
- **requires `SKY_AUTH_TOKEN_SECRET` to be ≥ 32 bytes** (Sky refuses to start
  otherwise);
- expects `SKY_CONSOLE_AUTH` to be set if you mount the console.

## More than one replica

The moment you run more than one instance, two things must be true:

- **A shared session store.** `memory` is per-process and `sqlite` is one file per
  host — neither is shareable. Switch `SKY_LIVE_STORE` to `redis` or `postgres`.
  The app code doesn't change.
- **Sticky sessions + cross-instance pub/sub.** The load balancer routes a
  session's requests to the same instance (keyed on the `sky_sid` cookie), and
  broadcasts reach users across replicas (`store=redis` wires this automatically).

That's the whole ladder: a single binary for the small case, and a couple of
environment variables — not a rewrite — when you grow. The reasoning behind each
setting is in the [Sky.Live architecture guide](../skylive/architecture.md) and the
[sky.toml + env reference](../sky-toml.md).

You've finished the core tour — from `sky init` to a deployed web app. The last two
chapters are for context: how Sky feels if you're arriving from another language,
and how to get an AI assistant to write good Sky with you.

**[Next → Coming from another language](18-coming-from-other-languages.md)**
