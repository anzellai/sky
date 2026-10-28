# 68-ai-dataset

A training dataset from support tickets, with `Std.Ai.Dataset`: load JSONL,
scrub personal data, drop repeated questions, validate the conversations,
split them by a seeded hash, freeze a content-addressed version, and load it
back with its digest checked. No model is involved, so it runs offline.

```
sky run src/Main.sky          # freeze data/tickets.jsonl into .sky/ai/datasets/tickets/
sky test tests/TicketsTest.sky
```

`src/Tickets.sky` holds the dataset, the limits, the split and the target, so
`Main` and the tests share them. Run it twice: the digest is the same, because
a version is named by the SHA-256 of its canonical rows. See
[`docs/ai/dataset.md`](../../docs/ai/dataset.md).
