# How to show Sky to your team

A 15-minute talk with a live demo. It is for an engineering team that has not
seen Sky before and wants to know two things: does it make full-stack work
faster, and does it stop bugs before they reach users.

You need three files from this folder:

| File | What it is for |
|---|---|
| `TALK.md` | This page: the plan, the timing and what to say |
| [`slides.md`](slides.md) | Ten slides for the parts you talk through |
| [`DEMO.md`](DEMO.md) | The live part, step by step, with the exact errors you will see |

## The plan

| # | Part | Time | Where |
|---|---|---|---|
| 1 | The problem | 1 min | slides 1 to 3 |
| 2 | The app | 2 min | browser, `DEMO.md` step 1 |
| 3 | The code | 3 min | editor, `DEMO.md` step 2, slide 5 |
| 4 | Break it | 3 min | editor, `DEMO.md` step 3, slide 6 |
| 5 | Tests and deploy | 2 min | terminal, `DEMO.md` step 4, slide 7 |
| 6 | What else Sky does | 2 min | slides 8 and 9 |
| 7 | Questions | 2 min | slide 10, the end of `DEMO.md` |

Part 4 is the one people remember. Do not cut it to save time. Cut part 6
first.

## What to say in each part

**1. The problem.** A normal web app uses a frontend language, a backend
language, SQL, an API layer and glue between them. Each boundary can drift: a
field renamed on one side and not the other, a status the UI does not know, a
`null` nobody checked. The tests catch some of these. Users find the rest.

**2. The app.** Two browser windows, side by side. Submit a claim in one, and
it shows in the other at once. Approve it, and the totals change in both.
Submit `abc` as the amount, and the form says what is wrong. Say: "This is a
full-stack app with a database and live updates. There is no JavaScript in
this project."

**3. The code.** Open `src/Main.sky`. `update` is every action the app can
take. `view` is the page. `subscriptions` is the live sync, one line. Then
open `src/Expense.sky`: one `Expense` type and one `codec`. The codec makes
the database table and the JSON. Run `curl localhost:8077/api/expenses` and
show the same field names.

**4. Break it.** Make the three changes in `DEMO.md`, one at a time. Each one
is a bug that ships in most stacks: a new status the UI does not handle, a
typo in a field name, and money treated as a float. Each time, `sky check`
stops and names the line. Say: "In most stacks, a customer finds these bugs.
Here, the build stops."

**5. Tests and deploy.** `sky test` runs the business rules in milliseconds,
with no database and no browser. `sky build` makes one binary. Copy it to a
server and run it. It needs no runtime and no `node_modules`.

**6. What else Sky does.** The same app builds for other targets (a wasm
client, desktop, mobile). `sky add` makes typed bindings for any Go module.
The standard library has auth, durable workflows and AI agents. Keep this
short. It is a list of reasons to try it, not a second demo.

**7. Questions.** The end of `DEMO.md` has answers to the questions people ask
most.

## Before the talk

- Run the whole talk once, with a timer.
- Install Sky and Go on the laptop you present from. Run the "Before the
  demo" block in `DEMO.md` on that laptop, on the day. The first build is the
  slow one, so do not let it happen in front of people.
- Make the editor and browser text large: 18 px or more.
- Put the two browser windows and the editor on one screen, so you do not
  switch spaces.
- Record a short screen video of parts 2 and 4. If the Wi-Fi or the laptop
  fails, play the video and keep talking.

## Show the slides

`slides.md` is plain Markdown, with a `---` line between slides. It reads well
on GitHub as it is. To present it as slides, use any Markdown slide tool. With
[Marp](https://marp.app):

```bash
npx @marp-team/marp-cli slides.md --html -o slides.html   # or --pdf, --pptx
```

The commands in the slides are the same commands the demo runs, so a slide
cannot promise something the app does not do.

## Change the demo for your team

The demo is small on purpose. Two changes make it fit a team better:

- Rename the sample claims in `src/Expenses.sky` (`seed`) to names your team
  knows.
- Use PostgreSQL if your team uses it: set `SKY_DB_PATH` to a
  `postgres://` URL. The code does not change.

Keep the three "break it" steps as they are. They are tested: each one is the
exact error `sky check` prints today.
