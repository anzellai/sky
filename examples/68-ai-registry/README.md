# 68 · The model registry

`Std.Ai.Registry` in one program: two versions of a fine-tuned adapter, each
registered with its lineage, gated on an eval suite, promoted, refused, forced
and rolled back, and the lineage of the alias that serving follows.

```bash
sky run src/Main.sky     # the lifecycle, printed step by step
sky test                 # serve the promoted adapter through a model slot (test mode)
```

- `src/Support.sky` holds the suite, the stand-in answers of each version,
  the adapter files (two small GGUF LoRA files the example writes itself; the
  training phase of the lifecycle makes real ones), the lineage, and the
  `chat` slot that serves `registry:support-lora@prod`.
- `src/Main.sky` runs the story in a fresh temporary directory: a SQLite file
  for the records and a local store for the files. A real app opens the
  registry on its own database (`Db.connect ()`), and the store moves to GCS
  or S3 with `SKY_AI_REGISTRY`.
- `tests/RegistryExampleTest.sky` runs under `sky test` in test mode
  (`.env.test`): the slot resolves `prod`, checks that the adapter was trained
  on the slot's base model, and sends the version's served name. The fixture in
  `tests/mocks/` answers only that name, so serving another adapter fails the
  test.

Guide: [`docs/ai/registry.md`](../../docs/ai/registry.md).
