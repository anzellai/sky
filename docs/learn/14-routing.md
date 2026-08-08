# Routing & navigation

A real app has more than one page. Sky.Live routes URLs to pages for you.

## A page type and routes

Model your pages as a union, then map URLs to them. The counter used a single
unit page; here's the real shape:

```elm
type Page
    = HomePage
    | AboutPage
    | PostPage String        -- carries a :slug from the URL


routes =
    [ route "/" HomePage
    , route "/about" AboutPage
    , route "/posts/:slug" PostPage    -- ctor : String -> Page
    ]

notFound = HomePage
```

Two rules:

- **A `:param` segment is captured and passed to the constructor** as a `String`.
  `PostPage` takes a `String`, so `/posts/hello` becomes `PostPage "hello"`.
- **Declaration order matters — literals before patterns.** Put `/posts/new`
  above `/posts/:slug`, or "new" matches as a slug.

Your `view` then branches on the current page, usually with a `case`.

## Links: always `sky-nav`

The most important navigation rule in Sky.Live: make internal links `sky-nav`
links.

```elm
import Std.Html as Html
import Std.Html.Attributes as Attr

Html.a
    [ Attr.href "/about", Attr.attribute "sky-nav" "" ]
    [ Html.text "About" ]
```

A `sky-nav` link is intercepted by the runtime: it fetches the new page and
patches the body over the **one** SSE connection the session already has. A plain
`<a href>` does a full page reload, which opens a *fresh* connection every time —
navigate a few pages and the browser's connection pool is exhausted and the tab
freezes.

So: `sky-nav` for every internal link; a bare `href` only when you mean to leave
the app entirely.

## Back and Forward just work

The runtime wires up the browser's Back/Forward buttons — you don't write anything
for history navigation. When you drive navigation from code (a `Navigate` message),
emit a small `data-sky-path` marker in your view and the runtime keeps the address
bar in step; the [Sky.Live guide](../skylive/overview.md) has the snippet.

**[Next → Data with Std.Db](15-data.md)**
