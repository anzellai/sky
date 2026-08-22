# Sky.Spa Todos — one client, one server, every platform

The whole point of Sky.Spa: you write **one** TEA app (Model / Msg / update /
view over `Std.Ui.Element`) and **one** stateless backend, and it runs on web,
desktop, and mobile with **no per-platform app logic**. Only the *shell* around
the client changes.

```
                     ┌───────────────────────────────────────────┐
                     │  ONE stateless backend  (server/)          │
                     │  Sky.Http.Server — typed /api + Server.static│
                     │  no session, no SSE; DB is the only state  │
                     │  (SQLite in dev · Postgres in prod)        │
                     └───────────────▲───────────────────────────┘
                                     │  HTTP: the client's wasm + the typed
                                     │  /api boundary (one shared Std.Codec)
        ┌────────────────┬───────────┼───────────────┬──────────────────┐
        │                │           │               │                  │
   ┌────┴────┐     ┌─────┴─────┐ ┌───┴────┐    ┌──────┴──────┐   ┌───────┴───────┐
   │ browser │     │ macOS     │ │ Win/Lin│    │  iOS/iPadOS │   │   Android     │
   │ (web)   │     │ WKWebView │ │ webview│    │  WKWebView  │   │   WebView     │
   │         │     │ desktop/  │ │ desktop│    │  (mobile-   │   │  (mobile-     │
   │         │     │ Webview.url│ │        │    │   ios/)     │   │   android/)   │
   └─────────┘     └───────────┘ └────────┘    └─────────────┘   └───────────────┘
        └──────────── all load the SAME wasm TEA client ─────────────┘
```

## The backend server — stateless, and shared by every shell

There is **one** backend binary (`server/`). It is **stateless**: no per-user
`Model`, no session, no SSE (that is Sky.Live's model). The *only* shared state
is the **database** (SQLite here, Postgres in production) behind the typed
boundary, plus auth. Because it's stateless it scales **horizontally** — run N
replicas behind a load balancer; the DB is the single shared axis. No sticky
sessions, no SSE fan-out.

It does two jobs:
1. serves the typed **API** (`/api/todos`, …) — the explicit boundary the client
   talks to, sharing one `Std.Codec` so client and server can't drift;
2. serves the client's **static assets** (`Server.static "/" "../public"`) so the
   **web** build is same-origin (no CORS), and — for desktop/mobile — is simply
   the URL each native shell points at.

## How each shell reaches the backend

Every shell loads the **same** client from the **same** backend URL — the only
thing that differs is what that URL is:

| Shell | Dev URL (this repo) | Prod URL |
|---|---|---|
| **Web** (browser) | `http://localhost:8951/` (same origin serves client + api) | `https://yourapp.com/` |
| **Desktop — macOS** (`Std.Webview.url`) | `http://127.0.0.1:8951/` | `https://yourapp.com/` |
| **Desktop — Win/Linux** (same, WebView2 / webkit2gtk) | `http://127.0.0.1:8951/` | `https://yourapp.com/` |
| **iOS/iPadOS** (WKWebView) | `http://localhost:8951/` (simulator shares host net) | `https://yourapp.com/` |
| **Android** (WebView) | `http://10.0.2.2:8951/` (emulator alias for host) | `https://yourapp.com/` |

So in **dev** every shell points at your local backend; in **prod** you change
**one URL constant** and every shell points at the one deployed backend.

## Two delivery models (dev uses the first; prod can use either)

1. **Load the whole client from the backend URL** — what this repo does. The
   native shell is a pure WebView; the wasm + assets come from `Server.static`.
   Simplest; the app is always in sync with the deployed client.
2. **Bundle the wasm client inside the native app** — the *mobile-embed* model
   Sky.Spa is designed for: ship `main.wasm` + `index.html` inside the .app /
   .apk / .ipa, load them from the bundle, and hit the backend **only** for
   `/api` calls. A one-time in-app wasm load (no per-visit download) → offline-
   first UI, and only *data* crosses the network. Point the shell's URL at a
   `file://`/bundled index instead of the backend, and set the client's API base
   to the deployed backend.

## Apple platforms

- **MacBook (macOS)** — already the **desktop** target: `Std.Webview.url` opens a
  native **WKWebView** window (macOS's system webview). `examples/60-spa-todos/desktop`.
- **iPhone / iPad (iOS/iPadOS)** — `mobile-ios/`: a SwiftUI + WKWebView shell.
  WKWebView runs wasm; the client is the identical one verified in macOS WKWebView
  and Android WebView. Build in Xcode (see `mobile-ios/README.md`).
