# CLAUDE.md — Sky Language Project

> **Entry point for Claude Code.** The coding guide for this project lives in
> **[AGENTS.md](AGENTS.md)** — it's agent-agnostic (Claude, Copilot, Cursor,
> Codex, …) and is imported below. Read it before writing any Sky.
>
> Keep AGENTS.md as the single source of truth; this file only exists so Claude
> Code picks the guide up automatically. `sky doc <Module>` is the live stdlib
> API (never drifts — generated from source); prefer it over any hand-copied
> table.

@AGENTS.md

## Claude Code — operational notes

Everything about *writing* Sky is in AGENTS.md. This section covers only what
Claude Code has to do differently because it runs commands in a session.

### `sky db start` leaves a process running after the command exits

With `[database] embedded = true`, the cluster's lifetime follows the verb:

- **`sky run` / `sky watch` are ephemeral** — they start this project's cluster
  and stop it when they exit (ref-counted, so two concurrent runs don't stop
  each other's database). Nothing to clean up.
- **`sky db start` is persistent** — it starts the cluster in `.skydata/pg/` on
  a unix socket and *returns*, leaving it up until `sky db stop`. That is the
  point (it's the mode for running `./sky-out/app` repeatedly), but it means the
  usual "the command finished, so nothing of mine is running" assumption is
  wrong here.

So: check with `sky db ps` (this project) or `sky db ps --all` (every cluster on
the machine) rather than assuming; and stop what you started with `sky db stop`
before declaring a task done, unless the user is going to keep working against
it. Never `pkill postgres` — a machine can carry one cluster per project and the
user may have a system PostgreSQL of their own, and `sky db stop` already
targets exactly this project's.

### Don't run `sky db provision` speculatively

`sky db provision --embed` fetches a PostgreSQL bundle over the network and pins
it; `--shared` provisions against a host cluster. Both are deliberate,
side-effecting acts an operator makes once — not something to try in order to
see what happens. Ask the user first.

`provision --embed` also **cannot succeed yet** — no bundle release has been
published, so it 404s. For a local PostgreSQL today, use `sky db start` with
`SKY_POSTGRES_BIN`, a local bundle, or a system install.

### Embedded PostgreSQL and an explicit DSN together are an error

The app never knows which tier it is in — it consumes a DSN, and only the
provisioner changes. So `embedded = true` (or `--embed`) alongside `path` /
`url` / `SKY_DB_PATH` / `DATABASE_URL` is refused *before the build*, not
resolved by precedence: preferring the cluster would have the app writing to a
throwaway local directory while the user believes it is talking to the server
they named. If you are handed a DSN, do not add `--embed` "to be safe" — pick
the tier the user asked for.
