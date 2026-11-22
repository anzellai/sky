# 78-i18n-booking

A booking page in several locales with `Std.I18n` and `Std.I18n.Format`.

```bash
sky run src/Main.sky
# http://localhost:8078/      English (en-GB)
# http://localhost:8078/zh    Simplified Chinese (zh-Hans)
# http://localhost:8078/ar    Arabic: the keys it lacks fall back to en-GB
# http://localhost:8078/xa    the pseudo-locale en-XA
```

- `sky.toml` lists the locales in `[i18n]`. The binary carries the CLDR data
  of these locales only.
- `i18n/<locale>/booking.json` holds the messages. `sky check` checks every
  file: change `{count}` to `{nights}` in `i18n/zh-Hans/booking.json`, or
  remove the `few` form from `i18n/ar/booking.json`, and the check fails
  with E5007 or E5008.
- The notice in the model is a `Text`, a key with typed arguments, so it
  renders in the locale of the page that shows it.

See [docs/i18n.md](../../docs/i18n.md).
