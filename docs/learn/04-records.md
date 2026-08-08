# Records

A record groups named fields together — think of it as a struct or an object,
but immutable.

## Give every record a type alias

Name the shape with `type alias`, then use the name everywhere:

```elm
type alias User =
    { name : String
    , age : Int
    }
```

Construct one with the same brace syntax:

```elm
ada : User
ada =
    { name = "Ada", age = 40 }
```

A tip that saves confusion: always name a record with a `type alias` and use the
name in signatures. Writing the raw `{ name : String, age : Int }` shape inline in
a function signature is awkward — the alias reads better and gives errors a name
to point at.

## Reading fields

Dot access, or the bare `.field` function:

```elm
who =
    ada.name                       -- "Ada"

names =
    List.map .name [ ada, ada ]    -- [ "Ada", "Ada" ]
```

## Updating is copying

Records are immutable. "Updating" a field produces a **new** record; the original
is untouched:

```elm
older : User
older =
    { ada | age = 41 }
```

`ada` is still `{ name = "Ada", age = 40 }`. `older` is a fresh value with every
other field copied across and `age` set to 41. This is the heart of functional
state: you never mutate, you derive a new value. It's exactly how you'll update
your app's model later — `{ model | count = model.count + 1 }`.

You can update several fields at once:

```elm
{ ada | name = "Ada L.", age = 41 }
```

**[Next → Unions & case](05-unions-and-case.md)**
