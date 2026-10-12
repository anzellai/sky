# 69-ai-scrub

Scrub support tickets before they become training data, with
`Std.Ai.Dataset.Scrub`: the built-in rules (secrets, email, IBAN, card number,
US SSN, UK NINO, phone), a pattern for the shop's own order numbers, and a
drop rule for staff-only notes. The report counts what was replaced and never
holds the replaced text. Pure Sky: it runs offline.

```
sky run src/Main.sky
sky test tests/RedactTest.sky
```

Every value in `src/Redact.sky` is made up; the card and IBAN are published
test numbers. See [`docs/ai/dataset.md`](../../docs/ai/dataset.md).
