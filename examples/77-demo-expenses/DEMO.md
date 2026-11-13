# Demo script: 10 minutes

Each step has what to do and what to say. Practise it once.

## Before the demo

```bash
cd examples/77-demo-expenses
rm -f expenses.db                  # start from the four sample claims
sky test tests/ExpenseTest.sky     # warms the build cache
sky run src/Main.sky
```

Open http://localhost:8077 in two browser windows, side by side. Open
`src/Main.sky` and `src/Expense.sky` in an editor.

## 1. The app (2 minutes)

- In window A, submit a claim: `Alan`, `Taxi`, `23.10`, Travel.
- It shows in window B at once. Approve it in window B. The totals change in
  both windows.
- Submit `abc` as the amount. The form says what is wrong.

Say: "This is a full-stack app with a database and live updates. Nothing here
is JavaScript."

## 2. The code (3 minutes)

- `src/Main.sky`: `update` is every thing the app can do. `view` is the page.
  `subscriptions` is the live sync, one line.
- `src/Expense.sky`: the `Expense` record and `codec`. That codec makes the
  database table and the JSON.
- Run `curl localhost:8077/api/expenses`. Same records, same field names.
- `src/Expenses.sky`: no SQL. The same code runs on PostgreSQL with
  `SKY_DB_PATH=postgres://...`.

Say: "One language, one type, one codec. The UI, the API and the table cannot
drift apart."

## 3. Break it (3 minutes)

Make each change, run `sky check src/Main.sky`, read the error, undo it.

**A new business state.** In `src/Expense.sky`, add `| Paid` to `Status`:

```
This `case` does not cover all cases — missing: Paid
```

The compiler names `statusLabel` and the `actions` view. Nothing ships until
every place handles the new state.

**A typo.** In `src/Main.sky`, change `expense.amount` to `expense.ammount`:

```
record is missing field(s): ammount
```

**Money as a float.** Change `Money.format expense.amount` to
`Money.format (expense.amount * 1.2)`:

```
`Money` is not a number: this needs an `Int` or a `Float`
```

Say: "In most stacks these are bugs a customer finds. Here the build stops,
and the error says where."

## 4. Tests and deploy (2 minutes)

- `sky test tests/ExpenseTest.sky`: the rules run in milliseconds, with no
  database and no browser.
- `sky build src/Main.sky` makes one binary. Copy it to a server and run it.
  It needs no runtime and no `node_modules`.

Say: "It compiles to Go. One small binary, fast to start, cheap to host."

## Questions people ask

- **Can we use our Go libraries?** Yes: `sky add <go module>` makes typed
  bindings. Each call returns a `Result`, so a Go error cannot crash the app.
- **Does it scale past one server?** Use PostgreSQL and a shared session
  store (`redis` or `postgres`). See `docs/skylive/architecture.md`.
- **Can it run as a mobile or desktop app?** The same `App.app` builds for
  `--target web:app`, `desktop` and `mobile:ios`. See `docs/skyapp/overview.md`.
