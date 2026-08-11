# 03 — Language Reference (the compat contract)

This is the **precise specification of the Sky language** the Rust compiler must
implement. It is the contract every other doc targets: if a program parses,
type-checks, and lowers under the Haskell compiler, it must do so identically
under the Rust one, and vice versa. **Accept/reject behaviour is normative** —
where a rule looks quirky, it is relied upon by the 42 examples and reproduced
verbatim (goal 00 §"Compat first").

Citations are `file:line` into the current Haskell tree at
`/Users/anzel/works/playground/sky`. They pin the exact behaviour; a
parser/typer author should not need to re-read the Haskell.

Two AST layers are referenced throughout:

- **Source AST** — raw parse tree, `src/Sky/AST/Source.hs`. The Rust analogue is
  the typed view over the rowan CST (doc 04).
- **Canonical AST** — name-resolved, desugared, `src/Sky/AST/Canonical.hs`. The
  Rust analogue is `hir` (doc 05). Several constructs (operators, interpolation,
  head-alias unfolding) exist only in Source and are *desugared away* into
  Canonical — those desugarings are part of this contract.

---

## 1. Lexical structure

### 1.1 Encoding, layout, whitespace

- Source is UTF-8 text. Columns are **1-based**; a tab advances the column by
  **4** (`src/Sky/Parse/Space.hs:37,69`). Newline resets column to 1
  (`Space.hs:72`). Rows are 1-based.
- **The layout is off-side-rule, column-based** — there is no layout-token
  insertion pass; the parser threads an `_indent` reference column in its state
  (`src/Sky/Parse/Primitives.hs:54`) and blocks continue while the current
  column stays past it.

| Primitive | Rule | Source |
|---|---|---|
| `spaces` | skip `' '`, `'\t'`, and `--` line comments, **but stop at newline** (so layout-sensitive callers see the line boundary). Block comments NOT skipped here. | `Space.hs:21-49` |
| `freshLine` | skip ALL whitespace incl. newlines, `--` line comments, nested `{- -}` block comments. Used when the parser may cross line boundaries. | `Space.hs:53-91` |
| `checkIndent` | succeed iff `col > indent` (strictly). The continuation test. | `Space.hs:119-123` |
| `checkAligned` | succeed iff `col == indent`. Sibling-item alignment (e.g. `let` bindings, `case` arms). | `Space.hs:135-139` |
| `withIndent n p` | run `p` with `_indent := n`, restore on exit. Callers set the block reference to the body's start column. | `Primitives.hs:193-200` |

**Model in one line:** each construct's body parses with `_indent` set to that
body's start column; tokens continue the body while `col > indent`; sibling
items align at `col == indent`; crossing newlines needs `freshLine`, inline
parsing uses `spaces` (halts at newline to preserve the boundary). Rust must
reproduce this column arithmetic (including tab=4) exactly — indentation changes
parse results.

### 1.2 Comments

| Form | Rule |
|---|---|
| `-- line` | to end of line. Recognised inside `spaces`/`freshLine`; a `--` after a token is a comment, never subtraction (`Space.hs:38-48`). |
| `{- block -}` | **nestable** (depth-counted, `Space.hs:95-116`). Only skipped by `freshLine`, not `spaces`. |

Comments are whitespace to the grammar. A separate post-parse raw-text scan
(`collectComments`, `src/Sky/Parse/Module.hs:103-187`) re-attaches every comment
with kind (`CommentLine`/`CommentBlock`) + position (`CommentOwnLine`/
`CommentTrailing`) + column for the formatter. The Rust CST keeps comments as
trivia in-tree (doc 04) — same information, no second scan.

### 1.3 Identifiers

Lexed by first character (`src/Sky/Parse/Variable.hs`):

| Class | Start char | Continue | Meaning | Source |
|---|---|---|---|---|
| **lower** | `_`, Unicode lowercase, or caseless letter (CJK/Arabic/Hebrew — `isLetter && not isUpper`) | `isAlphaNum \|\| '_'` | value / function / field / type-var names | `Variable.hs:17-35,53-54` |
| **upper** | ASCII/Unicode uppercase | same | type / constructor / module-segment names | `Variable.hs:39-49` |

- A lower identifier that is a **keyword is rejected** as an identifier
  (`Variable.hs:26-27`). Keyword set (`src/Sky/Parse/Keyword.hs:10-19`):
  `if then else case of let in type alias module import exposing as foreign True
  False`. Note `then`/`else`/`in`/`of`/`alias` are contextually reserved;
  `True`/`False` are keywords (boolean literals), not constructors.
- Caseless-letter identifiers (Chinese/Japanese/Korean, etc.) are **value-level
  only** — a user type must start with an ASCII (or explicitly uppercase
  Unicode) letter.
- **Qualified names**: `Upper(.Upper)*.name` — dotted chain of uppercase module
  segments then a final lower-or-upper name (`Variable.hs:59-81`). In expression
  position this is `Src.VarQual mod name` (only ONE module segment is captured
  by the expression atom, see `Expression.hs:417-424`); deeper module paths are
  resolved at canonicalise time.

### 1.4 Numeric literals

`src/Sky/Parse/Number.hs`. Produces `IntNum Int` or `FloatNum Double`.

| Form | Example | Result | Source |
|---|---|---|---|
| Decimal integer | `123` | `Int` | `Number.hs:52-56` |
| Hex integer | `0xFF`, `0x1a` | `Int` | `Number.hs:22-30` |
| Float w/ point | `1.5`, `123.456` | `Float` (needs digit after `.`) | `Number.hs:36-45` |
| Float w/ exponent | `1.5e-2`, `2.0E+10` | `Float` | `Number.hs:42-45,64-75` |
| **Integer w/ exponent** | `1e6` | **`Float`** (bare `1e6` is a Float, not Int) | `Number.hs:46-51` |

- Exponent: `[eE][+-]?digit+` (`parseExponent`, `Number.hs:64-75`).
- No underscores, no binary/octal literals, no leading-`.` floats (`.5`
  is `Accessor "5"`? no — `.` then lower only; `.5` does not parse as a number).
  A `.` is only a decimal point when **followed by a digit** (`Number.hs:36-37`);
  otherwise it is field-access / accessor.
- Negative literals: there is no numeric-negative token; `-1` is `Src.Negate
  (Int 1)` produced at expression/pattern level (see §5.1, §6).

### 1.5 String & char literals

`src/Sky/Parse/String.hs`. Three lexer results: `SingleLine`, `MultiLine`,
`CharLit`.

**Single-line string** `"..."` (`String.hs:39-47,162-173`):
- Escapes are unescaped at lex time (`unescapeString`, `String.hs:59-85`):

| Escape | Value | | Escape | Value |
|---|---|---|---|---|
| `\n \t \r \\ \" \' \0` | usual | | `\a \b \f \v` | bell/backspace/formfeed/vtab |
| `\xHH` | 2-hex byte | | `\uHHHH` | 4-hex BMP code point |
| `\u{H..}` | 1–8 hex, full code point ≤ U+10FFFF, no surrogates | | **unknown `\X`** | kept **verbatim** as `\X` (so a wrong escape is visible at compile time) |

  Code-point validity: `0 ≤ n ≤ 0x10FFFF`, not a surrogate `D800..DFFF`
  (`String.hs:120-122`). A string cannot span a raw newline.

**Char literal** `'c'` (`String.hs:126-156`): single char or one escape
(`\n \t \r \\ \'`); other escapes kept as `\X`. Stored as a `String` payload
(`Src.Chr String`), not a Rust `char`.

**Triple-quoted multiline string** `"""..."""` (`String.hs:22-37,177-188`):
- Everything between the opening `"""` and the first closing `"""` is captured
  **raw and unescaped** (`findTripleClose`), including newlines. Stored as
  `Src.MultilineStr String`.
- **Interpolation and escaping are applied later, at canonicalise time** (§1.6).

### 1.6 Multiline interpolation `{{expr}}` — desugaring contract

Applied in the **canonicaliser**, not the parser
(`src/Sky/Canonicalise/Expression.hs:42-47,529-651`). This is a normative
desugaring the Rust `hir` layer must reproduce.

`desugarMultiline` (`Expression.hs:541-555`) splits the raw string into
alternating literal / expression chunks (`splitInterpolation`,
`Expression.hs:573-593`), converts each, and left-folds with `++`:

```
"""hello {{name}}! you are {{age}} years old"""
  ⟹  "hello " ++ Debug.toString name ++ "! you are " ++ Debug.toString age ++ " years old"
```

Exact rules:

| Rule | Behaviour | Source |
|---|---|---|
| Concat operator | `Can.Binop "++" Basics append` with hardcoded annotation `Forall [a] (a→a→a)` | `Expression.hs:550-555` |
| Every expr chunk | wrapped in `Can.Call (VarKernel "Debug" "toString") [resolved]` — **even already-String exprs** | `Expression.hs:598-606` |
| Empty / single chunk | `Str ""` / the chunk verbatim (no `++`) | `Expression.hs:545-548` |
| Type constraint on `{{}}` body | **none** — `Debug.toString` accepts any type; no String requirement | (consequence of 598-606) |

**Allowed forms inside `{{...}}`** (body trimmed of spaces first,
`resolveInterpolationRef`, `Expression.hs:616-650`):

1. **Bare lower identifier** — resolved via env: top-level → `VarTopLevel`,
   kernel → `VarKernel`, else `VarLocal` (`Expression.hs:627-635`).
2. **Field access** `record.field` (lower before `.`) → `Can.Access (VarLocal
   record) field` (`Expression.hs:645-649`).
3. **Qualified** `Module.func` (Upper before `.`) → resolved through import
   alias to `VarKernel`; **unknown alias → literal fallback** `Str "{{...}}"`
   (`Expression.hs:636-644`).
4. **Single-arg call** — split on first space: `func arg` → `Can.Call func
   [arg]`, recursively (so `String.fromInt n`, `errorToString e` work)
   (`Expression.hs:619-624`).
5. Anything else → **literal fallback** `Str "{{...}}"` (`Expression.hs:650`) —
   the developer sees their source as a signal to simplify. This fallback is
   observable and must be reproduced.

> Note: the body is resolved by a hand-rolled splitter, **not** by re-invoking
> the real expression parser (the doc-comment at `Expression.hs:536-538` is
> stale). Multi-arg calls, operators, and parens inside `{{}}` do NOT parse —
> they hit the literal fallback.

**Escaping** (`splitInterpolation`, `Expression.hs:573-593`):

| Input | Output |
|---|---|
| `\{{` | literal `{{` (no interpolation) |
| `\\` | single literal `\` |
| `\X` (other) | verbatim `\X` (backslash preserved) |
| `{{` with no closing `}}` | treated as literal `{{`, scan continues |
| single `{` / `}` | literal (catch-all copy) |

Interpolation expressions ARE ordinary Canonical exprs → they flow into the type
checker as arguments to `Debug.toString` and `++`.

### 1.7 Operators & symbols

Operator chars: `+ - * / < > = ! & | ^ ~ % ? @ # $ : . \ '`
(`src/Sky/Parse/Symbol.hs:27-28`). The parser lexes a maximal run of these as
one operator token (`Symbol.hs:11-19`) but only a fixed set has meaning (§5.2).
There are **no user-defined operators** (§7).

---

## 2. Module structure

`src/Sky/Parse/Module.hs`. A module is: optional header, imports, then
declarations (`moduleParser`, `Module.hs:190-220`).

### 2.1 Header & exposing

```
module Sky.Core.List exposing (map, filter, List, Msg(..), Color(Red, Green), (|>))
```

- Header is optional. **No header ⇒ `exposing (..)`** (legacy/fixture behaviour,
  `Module.hs:204-209`).
- Module name: dotted uppercase segments (`Module.hs:244-260`).
- Exposing clause (`Module.hs:270-360`), items may span multiple lines with
  leading commas (canonical `sky fmt` shape):

| Exposed item | Source AST | Meaning | Source |
|---|---|---|---|
| `(..)` | `ExposingAll` | expose everything | `Module.hs:276-280` |
| `name` (lower) | `ExposedValue` | a value/function | `Module.hs:357-359` |
| `Type` | `ExposedType _ Private` | opaque type only | `Module.hs:348-349` |
| `Type(..)` | `ExposedType _ Public` | type + all constructors | `Module.hs:337-341` |
| `Type(A, B)` | `ExposedType _ (PublicCtors [..])` | type + selected constructors | `Module.hs:342-347` |
| `(+)` | `ExposedOperator` | an operator | `Module.hs:351-355` |

### 2.2 Imports

```
import Std.Db as Db exposing (Db, SqlValue(..))
import Sky.Core.Prelude exposing (..)
```

`Src.Import { name, alias : Maybe String, exposing }` (`Module.hs:386-436`):

- `as Alias` optional; alias must be **upper** or `_` (`Module.hs:402-418`).
- `exposing` optional; may put a newline between `exposing` and `(`
  (`Module.hs:421-436`). Absent `exposing` ⇒ empty list.
- Every non-aliased import ALSO registers its **last module segment** as an
  auto-qualifier (see §10). `import Sky.Core.Prelude exposing (..)` binds
  `Prelude.<name>` too.

---

## 3. Declarations

`src/Sky/Parse/Declaration.hs`. Five declaration kinds
(`DeclType`, `Declaration.hs:102-108`); the module builder splits them into
values / unions / aliases / infix (`Module.hs:448-490`).

### 3.1 Type alias

```
type alias Model = { count : Int, name : String }
type alias Cfg msg = { onSubmit : msg, label : String }
type alias Handler = Request -> Task Error Response
```

`Declaration.hs:135-144`. `type alias Name vars = TypeAnnotation`. Body may start
on the next line. Parametric aliases carry lowercase type-var params. A **record
alias** name doubles as a **constructor** (Elm convention): `Model { ... }` and
positional `Profile name age` both construct (`Declaration.hs:71-97`).

**Head-position alias unfolding** (closed limitation, canonical Elm shape): an
annotation whose head is an alias-of-a-function is unfolded before splitting args
— `view : Renderer Msg` where `type alias Renderer msg = Model -> Element msg`
peels correctly (`unfoldHeadAlias` in `Sky.Canonicalise.Module`; regression
`Sky.Canonicalise.HeadAliasFunctionSig`). Rust must unfold the head alias only
(argument/return leaf types keep nominal form).

### 3.2 ADT (union) type

```
type Msg = Increment | Decrement | SetName String | Move Int Int
type Color = Red | Green | Blue
```

`Declaration.hs:149-222`. `type Name vars = Ctor argType* (| Ctor argType*)*`.
Constructor args are **atomic types only** (no bare arrows/applications without
parens — `typeAtomForCtor`, `Declaration.hs:227-255`). The `=` and each `|` may
sit on continuation lines.

Canonical `Union` carries `CtorOpts` (`Canonical.hs:201-205`) computed from
shape — this classification is **observable in codegen** and must match:

| `CtorOpts` | Condition |
|---|---|
| `Enum` | all constructors zero-arg |
| `Unbox` | exactly one constructor, one arg |
| `Normal` | otherwise |

**Prelude-shadow rejection:** a user ADT whose type name OR constructor name
collides with a Prelude entry (`Int Float Bool String Char List Maybe Result
Task Error True False Just Nothing Ok Err`) is a **hard error** naming the
stdlib origin (audit §3.2; e.g. `type Result a = Just a | Nothing` rejected).

### 3.3 Value / function definitions & annotations

```
count : Int
count = 0

add : Int -> Int -> Int
add a b = a + b
```

`Declaration.hs:39-98`. A binding is `name pattern* = expr`. A preceding
`name : Type` annotation line is parsed separately (`DeclAnnotation`) and
re-associated to the following same-named value by the module builder
(`Module.hs:452-474`, `popAnnotation`). Unmatched annotations are dropped.

**Multi-line signatures** (closed limitation #10). Both continuation shapes
parse:

```
name              name
    : T               : T1
                      -> T2
```

The `:` may sit on a fresh-indented continuation line (`Declaration.hs:54-60`);
the `->` may sit on a fresh-indented continuation line inside the type
(`typeAnnotation`, `src/Sky/Parse/Type.hs:37-59`). Upper-named annotations (a
record-alias constructor's signature) get the same treatment
(`Declaration.hs:77-97`).

### 3.4 `foreign import` / infix

`foreign import "go/pkg"` is parsed to `DeclForeign` and currently **dropped** by
the module builder (`Declaration.hs:32-37,278-296`; `Module.hs:487-488`). Infix
fixity declarations exist in the Source AST (`Src.Infix`, `Source.hs:158-168`)
but the parser produces none today (`_binops` is always empty). The Rust
compiler needs neither for v1 compat; keep the AST slots for forward-compat.

---

## 4. Records

Records are structural (row-typed). Source expression forms
(`src/Sky/Parse/Expression.hs:304-354`):

| Form | Example | AST | Source |
|---|---|---|---|
| Literal | `{ x = 1, y = 2 }` | `Src.Record [(name, expr)]` | `Expression.hs:332-348` |
| Empty | `{}` | `Record []` | `Expression.hs:309-311` |
| Update | `{ model \| count = 0 }` | `Src.Update name fields` | `Expression.hs:324-331` |
| Field access | `record.field` (postfix, chainable) | `Src.Access` | `Expression.hs:241-250` |
| Accessor fn | `.field` | `Src.Accessor` | `Expression.hs:430-433` |

- Record update takes a **lower identifier** as the record (not an arbitrary
  expression): `{ r | f = v }`. Fields and value may span lines with leading
  commas.
- **Field order is canonicalised by `_fieldIndex`** (`Canonical.hs:166-170`);
  emission that depends on order must sort by it (non-regression rule §8).
- Record **type** annotations: `{ f : T, ... }` closed, or `{ r | f : T, ... }`
  row-polymorphic open record (`Type.hs:120-159`, `peekRowPolyIntro`
  `Type.hs:239-256`).

---

## 5. Expressions

Source AST: `Src.Expr_` (`Source.hs:172-199`). Canonical: `Can.Expr_`
(`Canonical.hs:72-98`).

### 5.1 Atoms, application, negative-literal args

- **Application** `f a b c` — juxtaposition, left-associative (`exprApp`,
  `Expression.hs:85-93`). Args may continue on lines indented past the function
  column OR past the block indent (`appArgsMultiline`, `Expression.hs:154-229`).
- **Parenthesised / unit / tuple** share `(` (`Expression.hs:256-287`): `()` →
  `Unit`; `(e)` → `Src.Paren e` (explicit grouping, blocks precedence
  re-flattening — see §5.2); `(e1, e2, ...)` → `Tuple`.
- **List** `[a, b, c]` — `Expression.hs:289-302`. Elements may span lines with
  leading commas.
- **Negative-literal argument** (closed limitation #4). In application-argument
  position, `-` immediately followed by a digit (no space) introduces a negative
  literal argument: `Math.atan2 0 -1` ⟹ `atan2 0 (-1)`
  (`appArgs`, `Expression.hs:121-149`; `peekNextIsNegativeDigit`,
  `Pattern.hs:272-277`). `f - 1` (spaces) stays subtraction. `f -x` (identifier)
  still needs parens `f (-x)` — Sky has no unary-negate on bindings.

### 5.2 Operators — precedence, associativity, desugaring

The parser emits a **flat** `Src.Binops [(operand, op)] final` without consulting
precedence (`Source.hs:187`). The **canonicaliser** flattens nested chains and
runs precedence-climbing (`canonicaliseBinops`,
`src/Sky/Canonicalise/Expression.hs:223-299`). `Src.Paren` is an
**opaque leaf** — parentheses are never flattened into the outer climb
(`Expression.hs:265-269`), so `(a - b) * c` keeps its grouping.

Precedence + associativity table (`src/Sky/Parse/Symbol.hs:40-62`). Each operator
desugars to a **kernel function call** (`Binop`, `resolveOpName`,
`Expression.hs:303-319`) — operators are not first-class beyond this:

| Op | Prec | Assoc | Desugars to | | Op | Prec | Assoc | Desugars to |
|---|---|---|---|---|---|---|---|---|
| `>>` | 9 | L | `Basics.composeL` | | `+` | 6 | L | `Basics.add` |
| `<<` | 9 | R | `Basics.composeR` | | `-` | 6 | L | `Basics.sub` |
| `^` | 8 | R | (`Basics` `^`) | | `++` | 5 | R | `Basics.append` |
| `*` | 7 | L | `Basics.mul` | | `::` | 5 | R | `List.cons` |
| `/` | 7 | L | `Basics.fdiv` | | `==` | 4 | N | `Basics.eq` |
| `//` | 7 | L | `Basics.idiv` | | `/=` | 4 | N | `Basics.neq` |
| `%` | 7 | L | (`Basics` `%`) | | `< > <= >=` | 4 | N | `lt gt le ge` |
| `\|>` | 0 | L | `Basics.apR` | | `&&` | 3 | R | `Basics.and` |
| `<\|` | 0 | R | `Basics.apL` | | `\|\|` | 2 | R | `Basics.or` |

- Unknown operators default to `Precedence 9 L` (`Symbol.hs:62`) and
  `Basics.<op>` (`Expression.hs:210`), but there is no way to define one, so this
  is dead surface.
- Non-associative (`N`) operators use `nextMin = p+1` — `a == b == c` climbs as
  left-nested (no chaining rejection today; reproduce the climb, not an
  Elm-style error).
- Operators may appear at the start of a continuation line indented past the
  block (pipeline shape, `binopRest`/`tryNextLineOp`, `Expression.hs:32-80`).

### 5.3 `if` / `then` / `else`

`Expression.hs:439-491`. `if c then a else b`, with `else if` chains folded into
`Src.If [(cond, then)] else` (a list of guarded branches + final else). The
`else if` lookahead treats `else if` as a unit (`elseIfChain`,
`Expression.hs:457-491`). There is no dangling-else ambiguity — `else` is
mandatory.

### 5.4 `let` / `in`

`Expression.hs:496-575`. `let binding+ in expr`. **All bindings must start at the
same column** (`bindingCol`, `letBindings`/`moreLetBindings`,
`Expression.hs:511-537`). Two binding forms:

| Form | Example | AST |
|---|---|---|
| Define | `x = e`, `f a b = e` | `Src.Define name pats body ann` |
| Destructure | `(a, b) = e`, `{ x, y } = r`, `Just x = m` | `Src.Destruct pat e` |

- Destructure uses a single pattern term with **no top-level cons** (so `x ::
  rest = list` stays a define, not a destructure) (`Expression.hs:560-574`).
- **Forward references are allowed**: `let a = b + 1; b = 5 in a` compiles
  (canonicaliser groups into `LetRec`; `Canonical.hs:88-90` has `Let`/`LetRec`/
  `LetDestruct`). Rust must do dependency grouping, not strict top-down scoping.
- **Auto-force of discarded Task bindings**: `let _ = TaskExpr` fires the effect
  (the lowerer wraps it in `rt.AnyTaskRun`) — see §8.

### 5.5 `case` / `of`

`Expression.hs:599-669`. `case subject of` then branches `pattern -> body`, each
branch aligned at `branchCol` (`caseBranches`/`moreCaseBranches`,
`Expression.hs:632-652`). Subject and `of` may be on separate lines
(`Expression.hs:600-627`). Each branch body binds `withIndent (max patCol
bodyCol)` so a following sibling arm is not slurped into the previous body
(`Expression.hs:656-669`). Exhaustiveness is checked and **enforced** (§11).

### 5.6 Lambda

`Expression.hs:369-377`. `\p1 p2 -> body`. Params are patterns; body may be on
the next line.

### 5.7 Tuples & unit

`()` is `Unit`. `(a, b)` and `(a, b, c, ...)` are `Tuple e1 e2 [rest]`
(`Source.hs:198`). The Canonical form and the type form
(`Type.hs`, `TTuple a b [rest]`) both carry ≥2 elements. Runtime fast-path
targets 2- and 3-tuples (`Tuple1` in `Sky.Type.Type`), but larger tuples parse.

---

## 6. Patterns

`src/Sky/Parse/Pattern.hs`; Source `Src.Pattern_` (`Source.hs:240-256`),
Canonical `Can.Pattern_` (`Canonical.hs:116-137`). Patterns appear in: case
arms, function/lambda params, let destructure.

| Pattern | Syntax | AST | Source |
|---|---|---|---|
| Wildcard | `_` | `PAnything` | `Pattern.hs:60-68` |
| Variable | `x`, `_foo` | `PVar` | `Pattern.hs:192-194` |
| As-alias | `pat as name` | `PAlias` | `Pattern.hs:43-52` |
| Unit | `()` | `PUnit` | `Pattern.hs:78-80` |
| Tuple | `(a, b, ...)` | `PTuple` | `Pattern.hs:86-93` |
| List | `[a, b]` | `PList` | `Pattern.hs:100-112` |
| Cons | `x :: rest` (right-assoc) | `PCons` | `Pattern.hs:33-42` |
| Constructor | `Just x`, `Nothing`, `Db.SetField v` (qualified) | `PCtor` / `PCtorQual` | `Pattern.hs:136-148` |
| Record | `{ a, b, c }` | `PRecord` (field names) | `Pattern.hs:115-120` |
| Int / neg-Int | `3`, `-3` | `PInt` | `Pattern.hs:151-168` |
| Float / neg-Float | `3.14`, `-3.14` | `PFloat` | `Pattern.hs:151-168` |
| String | `"foo"` (also accepts `"""..."""`) | `PStr` | `Pattern.hs:170-178` |
| Char | `'c'` | `PChr` | `Pattern.hs:180-184` |
| Bool | `True` / `False` | `PBool` | `Pattern.hs:186-190` |

- Negative-number patterns require `-<digit>` two-char lookahead so `-` inside
  `->` is not misconsumed (`peekNextIsNegativeDigit`, `Pattern.hs:151-162`).
- Qualified ctor patterns (`Mod.Ctor pat*`) mirror expression grammar (#584)
  so `case x of ( col, Db.SetField v ) -> …` parses without importing the ctor.
- Constructor args are atomic patterns; nested ctor patterns need parens.

---

## 7. Type system surface

HM (Hindley-Milner) inference with a small set of Elm-style built-in constrained
type variables. Internal representation `src/Sky/Type/Type.hs`; canonical types
`Canonical.hs:155-181`.

### 7.1 Types that exist

| Type form | Syntax | AST |
|---|---|---|
| Function | `a -> b` (right-assoc) | `TLambda` |
| Type var | `a`, `msg`, `comparable` | `TVar` |
| Applied constructor | `List Int`, `Result Error a`, `Maybe (Dict String String)` | `TType mod name args` / `TTypeQual` |
| Record (closed) | `{ x : Int, y : String }` | `TRecord fields Nothing` |
| Record (open / row-poly) | `{ r \| x : Int }` | `TRecord fields (Just r)` |
| Unit | `()` | `TUnit` |
| Tuple | `(a, b)`, `(a, b, c)` | `TTuple` |
| Alias | resolved | `TAlias mod name args aliasType` (`Canonical.hs:162`) |

Type application binds tighter than `->`; parenthesise applied args
(`Maybe (Dict String String)`).

### 7.2 Built-in constrained type variables (NOT typeclasses)

`Sky.Type.Type` `SuperType` (`Type.hs:61-67`). Certain **type-variable names**
carry a built-in constraint, resolved structurally by the unifier — there is no
user-facing class mechanism:

| Var name family | `SuperType` | Admissible types |
|---|---|---|
| `number` | `Number` | `Int` or `Float` |
| `comparable` | `Comparable` | types supporting `== < >` |
| `appendable` | `Appendable` | `String` or `List a` |
| `compappend` | `CompAppend` | `String` or `List comparable` |

Content variants (`Type.hs:51-58`): `FlexVar`/`FlexSuper` (inferred),
`RigidVar`/`RigidSuper` (from user annotation — a rigid var cannot unify with a
concrete type), `Structure`, `Alias`, `Error` (recovery). Annotations quantify
free lowercase vars (`Forall vars ty`, `Canonical.hs:180`); **`any` is special**
(§7.4).

### 7.3 Intentional omissions (reject / absent — reproduce exactly)

| Omitted | Behaviour | Ref |
|---|---|---|
| Higher-kinded types | HM only; no `f a` where `f` is abstracted | limitation #1 |
| Type classes / traits | none — only the 4 built-in super-vars above | — |
| Custom operators | none; operator set is fixed (§5.2) | limitation #3 |
| `where` clauses | none; use `let..in` | limitation #2 |
| GADTs, existentials, rank-N | none | — |

### 7.4 `any` — wildcard soundness gate (load-bearing)

`any` is a magic type name with **per-occurrence** wildcard semantics, NOT a
normal polymorphic var. Rules the typer must keep (CLAUDE.md "Wildcard-`any`
soundness gate"):

- `freeTypeVars` collects every type-var name **including `"any"`**;
  `Instantiate.fromAnnotation` filters `"any"` out and gives each occurrence its
  own fresh unification var.
- Any "is this annotation polymorphic?" gate must test `any (/= "any")
  freeVars`, **not** `not (null freeVars)`. Mis-gating treats wildcard-only sigs
  as polymorphic and diverges body↔caller vars under per-call-site
  re-instantiation, silently accepting wrong return types.

### 7.5 Strict-HM arity gate (closed limitation #7 — reject behaviour)

A zero-arg-typed binding called with an argument, or a `() -> X`-typed binding
referenced bare in a value slot, is a **hard error `[E2007]`**
(`Sky.Type.Constrain.Expression`; `typeE_ArityMismatch = "E2007"`,
`Diagnostic.hs:205-206`). Message names declared arity D vs supplied arity S:

- `println (Uuid.v4 ())` where `Uuid.v4 : Task Error String` (0-arg) →
  `[E2007] … Uuid.v4 declared as 0-arg, called with 1 args.`
- `doNow : Task Error Int; doNow = Time.now` (where `Time.now : () -> ...`) →
  `[E2007] … Time.now declared as 1-arg, called with 0 args.`

Wildcard-`any` sigs are exempt (real polymorphism preserved). The `Sky.Core.Pure`
module provides `() -> Task Error a` companions for a uniform call shape.

---

## 8. The effect boundary (Task-everywhere)

Single rule: **every observable side effect returns `Task Error a`.** Tiers
(CLAUDE.md "Effect boundary"):

| Tier | Type | Examples |
|---|---|---|
| Pure | bare `a` | `String.length`, `List.map`, `Crypto.sha256`, `System.getenvOr` |
| Fallible-pure | `Result e a` / `Maybe a` | `String.toInt`, JSON decoders, `Auth.hashPassword` |
| Effect | `Task Error a` | `File.*`, `Http.*`, `Db.*`, `Time.now`, `Random.*`, `Log.*`, most `System.*` |
| Diverging | `Int -> a` | `System.exit` (polymorphic return; never comes back) |

Error type is **`Sky.Core.Error`** (`sky-stdlib/Sky/Core/Error.sky`), a closed
ADT — never `String` (non-regression rule §8: no `Result String a` /
`Task String a` in public surfaces). Bridges: `Task.fromResult`,
`Task.andThenResult`, `Result.andThenTask`, `Task.mapError`, `Task.onError`
(`sky-stdlib/Sky/Core/Task.sky`).

**Auto-force of `let _ = TaskExpr`** (`src/Sky/Build/Compile.hs` around
`19626`): a discarded `let _ = <TaskExpr>` binding is wrapped in `rt.AnyTaskRun`
so the effect fires. This is a lowering behaviour, but it is **observable
semantics** — the Rust lowerer must reproduce it.

**Top-level `Task.run`**: a module-level binding of a Task value still needs an
explicit `Task.run` (runs at binding-init time), e.g.
`apiKey = System.getenv "K" |> Task.run |> Result.withDefault ""`.

---

## 9. `main` entry points + auto-force

- The generated Go `func main()` wraps the entry expression in
  **`rt.AnyTaskRun` unconditionally** (`Compile.hs` ~`19155-19170,21077`). So a
  Task-typed `main` runs with **no trailing `|> Task.run`** — the trailing
  `Task.run` at program entry is a no-op.
- All app-shape entries rely on this: `main = Cli.program cfg`,
  `main = Tui.app cfg`, `main = Webview.app cfg`, `main = Live.app cfg`.
- The Sky binding `main` in `module Main` emits as Go `func main()` (special-
  cased, not module-prefixed) (see §12).
- Module-level `Task.run` at a non-main binding is still load-bearing (§8).

---

## 10. Import qualifier resolution

`src/Sky/Canonicalise/Module.hs`. Every non-aliased `import M exposing (…)` also
registers `M`'s **last segment** as an auto-qualifier. Two imports may both try
to bind the same qualifier; resolution is the **explicit-alias-wins** rule
(`effectiveQualifier`, `Module.hs:976-991`; claims built by
`buildExplicitAliasClaims`, `Module.hs:950-956`):

| Situation | Result |
|---|---|
| Import has `as Alias` | binds `Alias` unconditionally (`Module.hs:978-979`) |
| Bare import, last-seg not claimed by a different-module explicit alias | binds last segment (`Module.hs:988`) |
| Bare import whose last-seg IS claimed by an explicit alias for a **different** module | auto-qualifier **suppressed** → `Nothing`; explicit alias wins. Exposed names still land unqualified (`Module.hs:985-987`) |

Worked example: `import Std.Db as Db` + `import Lib.Db exposing (conn)` → `Db.x`
resolves to `Std.Db`; `conn` (unqualified) resolves to `Lib.Db`; the bare `Db`
shortcut for `Lib.Db` is dropped silently.

**Same-module double import is always fine**: `import Std.Ui as Ui` + `import
Std.Ui exposing (Element)` — both resolve to the same canonical path, so the
suppress guard (`claimedPath /= importPath`) is false and the collision gate
counts one distinct source (`Module.hs:986,1041,1045-1046`).

**E1001 collision** (`detectImportAliasCollisions`, `Module.hs:994-1088`) fires
only when a qualifier has **≥2 distinct canonical sources**:

- two **bare** imports auto-registering the same last-segment for different
  modules, or
- two **explicit `as X`** aliases for different modules.

Message shape (`formatClash`, `Module.hs:1048-1088`):

```
<r>:<c>: Import error: two imports both bind the qualifier `<qualifier>`:
  - import <path1> (at r:c)
  - import <path2> (at r:c)
  Add `as <Alias>` to one of them, e.g. `import <lastPath> as <CamelCasePath>`.
```

The fix-it camel-cases the offending path (`App.State` → `AppState`). Kernel
paths are folded onto their pseudo-module so multiple kernel paths to one
dispatch table don't count as a collision (`Module.hs:1024`).

> Diagnostic code: canonicalise-phase legacy errors (including this one) surface
> under **`E1001`** (`canonE_UndefinedName`, `Diagnostic.hs:169`) via
> `legacyToDiag` (`Module.hs:146-167`) as a placeholder code; the message body
> is the text above with the `line:col:` prefix stripped. Precedence among
> canonicalise errors: alias-collision > import-hiding > prelude-shadow >
> ambiguous-use > unbound (`Module.hs:392-405`).

---

## 11. Exhaustiveness semantics

`src/Sky/Type/Exhaustiveness.hs`; wired in `Compile.hs:4193-4214`, gated at
`4977-4994`.

- **Hard compile error, not a warning.** A non-exhaustive `case` produces
  `[E3001]` (`exhaustE_NonExhaustive`, `Diagnostic.hs:210-211`) and the build
  gate returns `Left ("Non-exhaustive patterns: …")` — **no codegen, non-zero
  exit** (`Compile.hs:4988-4994`).
- Message: `Non-exhaustive case expression. Missing pattern(s): <names>` plus a
  hint `This case does not cover: … Add the missing branch(es) or use _ -> …`.
- **Only top-level case-arm heads are analysed** (`checkBranches`,
  `Exhaustiveness.hs:100-119`; `classify`, 129-158). Branch bodies are recursed
  separately.

Coverage rules (reproduce exactly — conservative, no false positives):

| Head shape | Rule |
|---|---|
| Wildcard `_`, var, **as-alias at head** | immediately exhaustive (`Exhaustiveness.hs:116-119`) |
| Constructor (`PCtor`) | must cover every ctor of the union's `_u_alts`; missing ctors reported by name (`105-107,136-142`) |
| Bool (`PBool`) | must have both `True` and `False`, else report missing (`108-112,143`) |
| Unit (`PUnit`) | always exhaustive (`113,154-155`) |
| Literal `PInt`/`PStr`/`PChr` | **always `Missing ["_"]`** unless a wildcard arm exists — infinite space needs `_` (`114,145-147,157-158`) |
| `PFloat`, List/`PCons`/Tuple/Record heads | **not checked** — fall through catch-all; contribute nothing to coverage. Effectively exhaustive if no ADT/Bool/Lit head and no wildcard (`103-104,132-134,148`) |

- **No redundant/unreachable-branch detection.** `E3002`
  (`exhaustE_RedundantArm`) is defined but **unused** — do not emit it in v1.

---

## 12. Go reserved-name rewriting (codegen compat)

Not a Sky-language rule, but observable in emitted Go and required for
example-run parity. Every Sky identifier in `reservedGoNames`
(`src/Sky/Build/Compile.hs:9766-9785`) is rewritten with a trailing `_` at
codegen (`Compile.hs:9623`). The list: `init`; predeclared funcs (`new make len
cap copy append delete panic recover print println clear min max complex imag
real close`); all 23 keywords (`type func var const interface struct map chan go
defer goto fallthrough range return for switch case default break continue import
package select`); predeclared types/constants (`bool byte rune string error any
comparable`, every `int*`/`uint*`/`float*`/`complex*` size, `true false iota
nil`). Every top-level Sky binding is also module-prefixed (`Main_view`,
`Std_Ui_layout`), so the reserved list only matters for locals/params. `main` in
`module Main` is special-cased to Go `func main()` (§9). Full spec: doc 08.

---

## 13. Limitation ledger — observable status

Behaviour the Rust compiler must match. "Closed" items are the current
accept/reject; "active" items are current rejections/quirks. (CLAUDE.md "Active
limitations".)

| # | Item | Status | Observable behaviour to reproduce |
|---|---|---|---|
| 1 | Higher-kinded types | active | reject |
| 2 | `where` clauses | active | absent (use `let`) |
| 3 | Custom operators | active | fixed operator set (§5.2) |
| 4 | Negative-literal args | **closed** | `f -1` ⟹ `f (-1)`; `f - 1` subtraction; `f -x` needs parens (§5.1) |
| 5 | `Dict.toList` typed-key inline-only | **closed** | inline + let-bound both typed |
| 6 | Go interface satisfaction in `sky check` | **closed** | structural-implements axiom admits FFI interface pairs |
| 7 | Zero-arg arity | **closed** | `[E2007]` hard error (§7.5) |
| 8 | Recursive list ops O(N) stack | **closed** | 13/13 list ops constant Go stack (semantics unchanged) |
| 9 | Zero-arg `Css.*` need `()` | **closed** | `Css.zero`/`auto`/`none`/… are bare values |
| 10 | Multi-line signatures | **closed** | `:`/`->` on continuation lines parse (§3.3) |
| — | Head-alias function sig | **closed** | `view : Renderer Msg` unfolds (§3.1) |
| — | `type Result a = …` shadow | **closed** | hard reject Prelude type/ctor shadow (§3.2) |
| — | Unknown qualified name | **closed** | canonicaliser rejects with did-you-mean, not deferred to `go build` |

---

## 14. Diagnostic code registry (partial — for the reporter)

`src/Sky/Reporting/Diagnostic.hs`. Codes the Rust `diagnostics` crate must keep
stable (tests + LSP key off them):

| Code | Meaning | Ref |
|---|---|---|
| `E0001` | parse / syntax error | `Module.hs:36-37` |
| `E1001` | undefined name / (placeholder for) import alias collision & canonicalise-phase errors | `Diagnostic.hs:169`, §10 |
| `E2005` | record-update mismatch | `Diagnostic.hs:195` |
| `E2006` | function arity (generic HM) | `Diagnostic.hs:197-198` |
| `E2007` | strict-HM arity mismatch (declared D vs supplied S) | `Diagnostic.hs:205-206`, §7.5 |
| `E2008` | unsupported `Dict` key — a `Dict` keyed by anything other than `String`/`Int`/`Float`/`Char`/`Bool`. **Rust-only, no oracle counterpart**: the oracle accepts these and panics at runtime (`rt.Dict: unsupported key type`), which "if it compiles, it works" forbids. Silent on a key type that is not concrete, so a key-polymorphic `Dict k v` is unaffected | `ty/src/dictkey.rs`, `ty/src/check.rs` |
| `E3001` | non-exhaustive case (**hard error**) | `Diagnostic.hs:210-211`, §11 |
| `E3002` | redundant arm — **defined, unused** (do not emit) | `Diagnostic.hs:213-214` |
| `E4001` | typed kernel call with `any`-typed primitive arg | `Diagnostic.hs:218-219` |

The Rust compiler is **compat-first** (goal 00 §"Compat first"): where the above
is quirky-but-relied-upon (interpolation `Debug.toString`-wrap, explicit-alias-
wins, `main` auto-force, literal-fallback interpolation, conservative
exhaustiveness), reproduce it, then improve behind a documented change — never
silently diverge. The Haskell compiler is the differential oracle (doc 11).
