# Lists

A list is an ordered sequence of values that all share one type. There is no
`for` loop in Sky — you transform lists with functions.

```elm
nums : List Int
nums =
    [ 1, 2, 3, 4 ]
```

All elements must be the same type: `[ 1, 2, 3 ]` is fine, `[ 1, "two" ]` is not.

## Building lists

Prepend with cons, `::`:

```elm
more =
    0 :: nums            -- [ 0, 1, 2, 3, 4 ]
```

Or generate a range:

```elm
oneToTen =
    List.range 1 10      -- [ 1, 2, …, 10 ]
```

## Transforming instead of looping

The three workhorses: `map` (transform each element), `filter` (keep the ones that
pass a test), and `foldl` (collapse the list to a single value).

```elm
doubled =
    List.map (\x -> x * 2) nums          -- [ 2, 4, 6, 8 ]

evens =
    List.filter (\x -> modBy 2 x == 0) nums   -- [ 2, 4 ]

total =
    List.foldl (\x acc -> x + acc) 0 nums     -- 10
```

`foldl` is the general one: it walks the list left to right, carrying an
accumulator. Summing a shopping cart is just a fold:

```elm
cartTotal : List { price : Int } -> Int
cartTotal cart =
    List.foldl (\item acc -> acc + item.price) 0 cart
```

Anything you'd reach for a loop for — summing, counting, building a new
collection — is a `map`, `filter`, or `foldl`.

## More in the box

`List.head`, `List.reverse`, `List.length`, `List.member`, `List.any`,
`List.all`, `List.concat`, `List.take`, `List.drop`, and more. Look them up with
`sky doc Sky.Core.List`, or browse the [List module](../m/Sky.Core.List.html).

One catch: `List.head` might be handed an empty list, so it returns a `Maybe` — a
"value or none". That's the next lesson.

**[Next → Maybe & Result](07-maybe-and-result.md)**
