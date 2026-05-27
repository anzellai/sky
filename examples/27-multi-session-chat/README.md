# Multi-session chat — `Std.Cmd.publish` + `Std.Sub.subscribeTopic`

Chatroom demo proving Sky.Live's pub/sub stack end-to-end: two browser
tabs in the same room exchange messages in real time without polling.

## Build & run

```bash
sky build src/Main.sky
./sky-out/app
```

The server listens on port 8000. Open two browser tabs at
`http://localhost:8000/chat/lounge`, type a message in one, watch it
arrive in the other within ~100 ms.

## What's interesting

- **`Sub.subscribeTopic ("chat:room-" ++ room) MessageReceived`** —
  each session subscribes to the room's topic in `subscriptions`.
- **`Cmd.publish topic payloadDict`** in the `SendMessage` handler —
  fire-and-forget broadcast.
- **DB write first, broadcast second** — the row is the source of
  truth (durable; a late joiner reads it from `loadRoomHistory` in
  `init`); the broadcast is the low-latency hint. If the publish is
  lost (process crash, network blip), the next page load still sees
  the message.
- **Echo-to-publisher** — the sender's own subscription receives the
  broadcast too. Matches Redis / NATS / MQTT semantics.

## Verifying

The Playwright probe drives the multi-tab flow + asserts both
directions + echo + cross-room isolation:

```bash
bash scripts/verify-pubsub-multitab.sh
```

Expected output:

```
PASS verify-pubsub-multitab
    A→B  latency_ms=<n>
    B→A  latency_ms=<n>
    echo latency_ms=<n>
```

All three latencies must be under 500 ms (intra-process SLA).

## Reference

- [`docs/skylive/pubsub.md`](../../docs/skylive/pubsub.md) — user-facing
  tutorial: API, decision matrix, durability pattern, migration from
  `Time.every` polling, cross-process broker tiers (v0.16+).
- [`docs/skylive/pubsub-design.md`](../../docs/skylive/pubsub-design.md) —
  architecture write-up: runtime, wire shape, lifecycle, broker
  interface seam.
