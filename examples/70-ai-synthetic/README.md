# 70-ai-synthetic

Generate a support FAQ dataset with a teacher model, with
`Std.Ai.Dataset.Synthetic`: topics and personas, a few-shot example, a seeded
plan, dedupe, a length filter, and `Std.Ai.Eval`'s judge to keep only good
answers. The kept rows are scrubbed and frozen into a dataset version whose
lineage names the generator, the teacher and the seed.

```
sky run src/Main.sky               # print the plan (every teacher call); offline
sky run src/Main.sky -- generate   # call the teacher and the judge, then freeze
sky test tests/FaqTest.sky         # the whole run, offline
```

The teacher is the `"teacher"` slot and the judge the `"judge"` slot. With
nothing set, both are the local pinned development model (Qwen2.5 0.5B
Instruct), fetched into your AI home on first use. Move either with the slot
variables, `SKY_AI_TEACHER_*` and `SKY_AI_JUDGE_*` (a URL and a key for a
hosted model, for example; see [`docs/ai/local.md`](../../docs/ai/local.md)).
A 0.5B teacher writes simple rows; a larger teacher writes better ones.

`sky test` runs in test mode because the project commits a `.env.test`: no
engine starts, and the teacher's and the judge's calls are answered by
`tests/mocks/` (one teacher fixture per topic, picked by `bodyContains`). See
[`docs/ai/dataset.md`](../../docs/ai/dataset.md).
