# Std.I18n catalogue errors (E5001 to E5008)

`sky check` and `sky build` read the `[i18n]` section of `sky.toml` and every
message catalogue under its folder. A problem stops the build before `go
build` runs. Each problem has a code and a location: `sky.toml:4`, or the
catalogue file and line (`i18n/fr/shop.json:12`).

The check is the runtime's own code (`runtime-go/rt/i18n`). The build
compiles it and runs it, so the build and the program read every message the
same way. See [docs/i18n.md](../i18n.md) for the module.

## E5001

**I18N CONFIG.** The `[i18n]` section is wrong.

- A key that `[i18n]` does not have. Its keys are `locales`, `default` and
  `catalogues`.
- `locales` is missing, empty, or not an array of strings.
- `default` is missing, is not in `locales`, or is the pseudo-locale `en-XA`.
- A locale is not written in canonical form. Write `en-GB`, not `en-gb`.
- A locale is listed twice (`zh` and `zh-Hans` are the same locale).

```toml
[i18n]
locales = ["en-GB", "zh-Hans"]
default = "en-GB"
catalogues = "i18n"     # optional, "i18n" by default
```

## E5002

**UNKNOWN LOCALE.** A locale in `locales`, or a catalogue folder name, does
not parse as a BCP 47 tag, or CLDR has no data for it. Use a tag that CLDR
has, such as `fr`, `fr-CA`, `pt-BR`, `zh-Hant-HK`.

## E5003

**CATALOGUE FILE.** A catalogue file or folder is wrong.

- The catalogue folder cannot be read.
- A file is not valid JSON, or its top level is not an object.
- A value is not a message string or an object (an array, a number).
- A key is defined twice: twice in one object, in two files of one locale,
  or as `"a.b"` and as `"a": { "b": ... }`.
- A folder is for a locale that is not in `[i18n] locales`, or its name is
  not in canonical form.
- A folder for `en-XA` exists. The build makes `en-XA` from the default
  locale: remove the folder.
- A `.json` file is not inside a locale folder.

## E5004

**MESSAGE SYNTAX.** A message does not parse. The message shows the
character where the problem is.

```json
{ "greet": "Hello {name" }
```

Close the placeholder: `"Hello {name}"`. To write a brace as text, escape
it: `\\{` in JSON (`\{` in the message).

## E5005

**UNKNOWN MESSAGE FUNCTION.** A placeholder names a function or an option
that Sky does not have. Sky has `:string`, `:number` (options
`minimumFractionDigits`, `maximumFractionDigits`, `minimumIntegerDigits`,
`useGrouping`, `select`) and `:integer` (options `minimumIntegerDigits`,
`useGrouping`, `select`).

```json
{ "when": "{$d :datetime}" }
```

Pass the value already formatted (`I18n.time ms zone`, or a string from
`Std.I18n.Format`) and write `{when}`.

## E5006

**KEY NOT IN DEFAULT LOCALE.** A locale has a key that the default locale
does not have. Every key starts in the default locale. Add the key there, or
remove it. A key that a non-default locale lacks is not an error: it falls
back through the chain to the default locale. `sky check` reports the
coverage of each locale.

## E5007

**UNKNOWN PLACEHOLDER.** A message uses a placeholder that the default
locale's message for the same key does not have. The code that renders the
key gives the arguments of the default message only, so the placeholder
would never have a value.

```json
en-GB: { "greet": "Hello {name}" }
fr:    { "greet": "Bonjour {nom}" }
```

Use the same names in every locale: `"Bonjour {name}"`.

## E5008

**PLURAL CATEGORIES.** A plural message does not fit the CLDR plural rules
of its locale.

- A category the locale uses has no form. Arabic uses `zero`, `one`, `two`,
  `few`, `many` and `other`. English uses `one` and `other`. Chinese and
  Japanese use `other` only.
- A form names a category the locale does not use (`few` in English). For
  the number 0 in English, write the exact key `"=0"`.
- A plural object has no `other`.
- A `.match` on a `:number` or `:integer` selector lacks a category. The
  catch-all `*` stands for `other`.

```json
{ "nights": { "=0": "No nights", "one": "{count} night", "other": "{count} nights" } }
```
