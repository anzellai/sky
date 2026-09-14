# 66 · slack-agent — a durable LLM agent bot for Slack

A 24/7 Slack bot. Mention it in a channel and it answers with an LLM agent that
runs as a **durable workflow**, then replies in the thread. The reply survives a
restart, and each paid model call runs exactly once.

This example is the capstone for `Std.Ai` + `Std.Durable`. It puts the whole
stack in one small app:

| Piece | Module | Role here |
|---|---|---|
| HTTP server | `Sky.Http.Server` | receives Slack Events API webhooks |
| Durable workflow | `Std.Durable` | the reply is resumable and exactly-once |
| LLM call | `Std.Ai.Provider` | the model turn (OpenAI-compatible, local models too) |
| Action firewall | `Std.Ai.Policy` | the outbound Slack post goes through a pure policy |
| Cost ledger | `Std.Ai.Trace` | per-run token + cost capture, priced in `Std.Money` |

## How it works

```
Slack  ──app_mention──▶  POST /slack/events  ──▶  Durable.start (ack 200 in <3s)
                                                        │
        background poller (Durable.poll) ◀─────────────┘
                                                        │
                                 ┌──────────────────────┼───────────────────────┐
                                 │  step "llm"   the model writes the reply       │
                                 │  step "reply" Policy.gate ▶ Slack chat.postMessage
                                 │  step "trace" Trace.record (tokens + cost)     │
                                 └────────────────────────────────────────────────┘
```

Each labelled step is journalled. If the process is redeployed mid-run, the
workflow resumes from the last completed step: the model is not called again, and
the reply is not posted twice.

**The firewall is the point.** The model proposes text; it never authorises the
side effect. A pure `firewall : Action -> Decision` decides whether the Slack post
runs. Here posting is `Low` risk and runs unattended. If the bot later grew a
tool the model could invoke above `Low` (say, running a command), the firewall
would hold it for human approval — `NeedsApproval` pairs naturally with
`Durable.awaitSignal`, so a human "approve" resumes the workflow. The LLM is
never the authorisation boundary.

## Run it

```bash
export OPENAI_API_KEY=sk-...
export SLACK_BOT_TOKEN=xoxb-...
sky run src/Main.sky
```

Then point a Slack app's Event Subscriptions request URL at
`https://<your-host>/slack/events` and subscribe to the `app_mention` bot event.
The `url_verification` handshake is handled for you.

`GET /healthz` is a DB-free liveness check.

## Tiers

It ships on **SQLite** so it boots with no external setup. `Std.Db` is
dialect-safe, so the same code runs on **embedded PostgreSQL** in production —
set `embedded = true` under `[database]` in `sky.toml`. The durable journal and
the trace ledger then persist across restarts on a real engine, and pgvector
long-term memory (`Std.Ai.Memory.Pg`) drops in on the same connection.

## Test it offline

The durable reply workflow is proven end-to-end with both HTTP boundaries mocked
(no network, no credentials) by the `slack-agent` fixture and
`rust/crates/sky/tests/slack_agent_flow.rs`: one mention runs the model step,
posts through the firewall, and captures the cost, all offline.
