---
marp: true
paginate: true
title: "Sky: one language for the whole app"
---

# Sky

## One language for the whole app

UI, server, database and API in one typed language.
If it compiles, it works.

---

# A normal web app has five parts

| Part | Usually |
|---|---|
| Frontend | TypeScript, React |
| Backend | Go, Node, Python, Java |
| Database | SQL, an ORM, migrations |
| API | REST or GraphQL, a generated client |
| Glue | validation, serialisation, state sync |

Each boundary can drift. A field is renamed on one side only. A new status
reaches a screen that does not know it. A `null` nobody checked.

---

# What Sky is

- An Elm-family language that compiles to **typed Go**
- **No null.** No exceptions. Every `case` must cover every value.
- **Every effect is a typed `Task`.** Errors are values, not surprises.
- `sky check` runs `go build` on the output: a type error in the generated
  code cannot reach production either
- **One binary** to deploy

---

# Demo: team expenses

A person submits a claim. A manager approves or rejects it.
Every open window updates at once. The same data is a JSON API.

- About 880 lines in three files, comments included
- No JavaScript, no SQL, no CSS file, no API client

*(switch to the browser)*

---

# One type drives everything

```elm
type alias Expense =
    { id : Int, who : String, description : String
    , category : Category, amount : Money, status : Status }

codec : Codec Expense      -- the table AND the JSON
```

- The database table comes from `codec`
- `GET /api/expenses` comes from `codec`
- The form decodes into a typed `Claim`. Only `validate` makes an `Expense`.

The UI, the API and the table cannot drift apart.

---

# Break it

| Change | What `sky check` says |
|---|---|
| Add `\| Paid` to `Status` | `missing: Paid`, at every `case` that must handle it |
| `expense.amount` to `expense.ammount` | `record is missing field(s): ammount` |
| `money (expense.amount * 1.2)` | `` `Money` is not a number `` |

In most stacks, a customer finds these bugs. Here, the build stops,
and the error names the line.

---

# Tests and deploy

```bash
sky test tests/ExpenseTest.sky   # the rules, in milliseconds
sky build src/Main.sky           # one binary
```

- Tests need no database and no browser: the rules are pure functions
- Copy the binary to a server. No runtime, no `node_modules`.
- An idle Sky web app uses about 25 MB of memory

---

# One source, many targets

```bash
sky build src/Main.sky                      # server-driven web app
sky build --target web:app src/Main.sky     # wasm client
sky build --target desktop src/Main.sky     # desktop window (macOS)
sky build --target mobile:ios src/Main.sky  # iOS app (needs Xcode)
```

Same `init`, `update` and `view`. The target is a build flag.

---

# Batteries, not dependencies

- **Data:** `Std.Db` on SQLite or PostgreSQL, migrations, exact `Std.Money`
- **Users:** `Std.Auth`, sessions, CSRF, pages that work under a strict CSP
- **Long work:** `Std.Durable` workflows that survive a restart
- **AI:** providers, durable agents, a policy firewall, evals
- **Go:** `sky add <module>` makes typed bindings for any Go package
- **Ops:** structured logs, OpenTelemetry, a built-in console

---

# Try it

```bash
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh
sky init hello && cd hello
sky run src/Main.sky
```

- Docs and tour: https://anzellai.github.io/sky/
- This demo: `examples/77-demo-expenses`

## Questions?
