# 77 · Team expenses: a full-stack app in one language

A person submits an expense claim. A manager approves or rejects it. Every
open browser window shows the change at once, and the same data is a JSON API.

It is about 640 lines of Sky in three files, comments included. There is no JavaScript, no SQL,
no API client and no CSS file. To show it to a team, follow [DEMO.md](DEMO.md)
(10 minutes).

## Run it

```bash
sky test tests/ExpenseTest.sky     # the business rules, offline
sky run src/Main.sky               # http://localhost:8077
curl localhost:8077/api/expenses   # the same data as JSON
```

The first start creates `expenses.db` (SQLite) with four sample claims. Set
`SKY_DB_PATH` to a `postgres://` URL and the same code runs on PostgreSQL.

## The files

| File | What it is |
|---|---|
| `src/Expense.sky` | The business rules, pure: the `Expense` record, the `Status` and `Category` types, `validate`, totals, and the ONE codec for the database and the JSON |
| `src/Expenses.sky` | The table: `Std.Db.Store` builds the SQL from the codec |
| `src/Main.sky` | The app: the view (`Std.Ui`), `update`, live sync between sessions, and `GET /api/expenses` |
| `tests/ExpenseTest.sky` | Tests for the rules |

## What it shows

| Claim | Where to look |
|---|---|
| One language for UI, server, database and API | `src/Main.sky` is the whole app. The browser runs no app code. |
| Live updates with no extra code | `Cmd.publish topic` after a write, `Sub.subscribeTopic topic` in `subscriptions` |
| One type, no drift | `Expense.codec` makes the table columns and the JSON shape |
| Exact money | `Std.Money`: `0.10 + 0.20` is `0.30`, and adding two currencies is an `Err`, never a wrong number |
| Bad input cannot reach the database | The form decodes into a typed `Claim`. Only `Expense.validate` makes an `Expense` from it. |
| No null, no forgotten case | `Status` is a closed type. A new status makes the compiler name every `case` that does not handle it. |
