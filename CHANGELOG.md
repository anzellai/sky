# Changelog

Notable user-visible changes. Keep this file additive — never rewrite history.

## Unreleased

### Sky.Live

- **Breaking — default HTML template no longer loads Inter from Google Fonts.** The shell document emitted by `Live.app` previously preconnected to `fonts.googleapis.com` / `fonts.gstatic.com`, fetched the Inter family, and forced `font-family: 'Inter' … !important` on `body` and `.font-sans`. All four lines have been removed.
  - **Why:** third-party request on every cold page load (offline dev, GDPR, every visitor's IP logged with Google), plus an `!important` rule that fought app-level typography. There was no opt-out.
  - **Behaviour now:** the `<head>` ships only `<meta charset>` and `<meta viewport>`. Headings and body inherit the browser default (Times/Arial) until the app sets typography itself.
  - **Migration:** apps that want a webfont add it explicitly — e.g. a `Html.styleNode` in the view's head fragment, a self-hosted `@font-face` in a `Css.stylesheet`, or a `<link>` served from `Server.static`. Apps that were silently relying on the default Inter will look unstyled until they set their own font.
  - **Privacy/a11y wins:** no third-party network request from the runtime, and no `!important` override blocking accessibility-first apps that self-host (e.g. Atkinson Hyperlegible).
