# [E2014] INVALID REGEX

A literal pattern given to `Regex.fromString` is not a valid RE2 regular
expression.

```text
-- INVALID REGEX ------------------------------------------ src/Main.sky:9:22 [E2014]

8 | price =
9 |     Regex.fromString "(\\d+"
  |                      ^^^^^^^

This regular expression is not valid RE2, so `Regex.fromString` would return
`Err` for it every time: error parsing regexp: missing closing ): `(\d+`.

Try: write the pattern in RE2 syntax (https://github.com/google/re2/wiki/Syntax).
In a triple-quoted string a backslash is kept as it is (`"""\d+"""`) and a
literal `{{` is written `\{{`; in a plain string write each backslash twice
(`"\\d+"`).
```

Added in v0.29.0.

## What it covers

`Regex.fromString : String -> Result Error Regex` compiles a pattern at run
time, and its `Err` carries the RE2 error. When the pattern is a **literal**,
the checker runs the same parse (Go's `regexp/syntax`) while the program
compiles, so a pattern that can never compile is an error with source context,
not an `Err` found later. The text after the colon is exactly what Go's
`regexp.Compile` says.

| Checked when the program compiles | Checked at run time (the `Result`) |
|---|---|
| `Regex.fromString "(\\d+"` | `Regex.fromString pattern` (a variable) |
| `Regex.fromString """(\d+"""` (no `{{ }}`) | `Regex.fromString """{{prefix}}\d+"""` (an interpolation) |
| `"(\\d+" \|> Regex.fromString` | `Regex.fromString (a ++ b)` |

The common RE2 refusals:

| Pattern | Error |
|---|---|
| `(` | missing closing ) |
| `[a-z` | missing closing ] |
| `*a` | missing argument to repetition operator |
| `a**` | invalid nested repetition operator |
| `a{3,2}`, `a{1001}` | invalid repeat count |
| `(a)\1` | invalid escape sequence (RE2 has no backreferences) |
| `(?=a)`, `(?<!a)` | invalid or unsupported Perl syntax (no look-around) |
| `[z-a]` | invalid character class range |

## Writing a pattern in Sky

A triple-quoted string keeps a backslash, so it reads like the pattern itself:

```elm
words : Regex
words =
    Regex.fromString """\b\w+\b""" |> Result.withDefault Regex.never
```

Two things are special inside a triple-quoted string:

- `\\` is one backslash, so `"""\\d"""` is the pattern `\d`, the same as
  `"""\d"""`.
- `{{` opens an interpolation (`{{name}}`). A pattern that must match a literal
  `{{` writes it `\{{`: `"""\{{(\w+)}}"""` matches `{{name}}`. A single `{`, as
  in `\d{2,4}`, is kept.

In a plain string every backslash is written twice: `"\\s+"` is `\s+`.

A pattern with an interpolation is not a literal, so it is not checked here; an
invalid result is the `Err` of `Regex.fromString` at run time.

## How to fix it

Correct the pattern. If you meant a character that RE2 treats as an operator,
escape it (`\(`, `\[`, `\.`, `\{`). RE2 has no backreferences and no
look-around; a pattern that needs them is usually two steps in Sky: match the
larger shape, then test the part with `String` functions or a second `Regex`.
