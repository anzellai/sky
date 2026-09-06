# Security Policy

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.**

Report privately, through either channel:

- **GitHub Security Advisory** (preferred) — open a private report at
  <https://github.com/anzellai/sky/security/advisories/new>. This keeps the
  discussion private with the maintainer until a fix is ready.
- **Email** — `security@sky-lang.org`

  > ⚠️ **Placeholder — maintainer to confirm.** Verify this address is
  > monitored (or replace it with the real disclosure contact) before relying
  > on it. If in doubt, use the GitHub Security Advisory link above, which does
  > not depend on an email inbox.

Please include, as far as you can:

- the affected component (compiler, runtime, a specific `Std.*` module, CLI);
- the Sky version (`sky --version`) and platform;
- a minimal reproduction — ideally a small `.sky` program or command;
- the impact you believe it has.

## What to expect

- **Acknowledgement** of your report as soon as the maintainer is able.
- An assessment of severity and affected versions, and a fix or mitigation
  plan.
- **Coordinated disclosure:** please give the maintainer a reasonable window to
  ship a fix before any public disclosure. Credit is given to reporters who
  wish to be named.

## Supported versions

Sky is **pre-1.0 (0.x)** and ships from a single active line. Security fixes
land on the **latest released 0.x line only**; there are no long-term-support
branches yet. Always upgrade to the newest release (`sky upgrade`) to receive
security fixes.

| Version | Supported |
|---|---|
| Latest 0.x line (currently **v0.23.x**) | ✅ |
| Any older 0.x release | ❌ (upgrade to the latest) |

See [`VERSIONING.md`](VERSIONING.md) for the full versioning and release policy,
and [`CHANGELOG.md`](CHANGELOG.md) for released security fixes (look for
`Security` / `⚠ Breaking` sections).

## Scope

In scope: the Sky compiler (`rust/`), the Go runtime (`runtime-go/`), the
stdlib (`sky-stdlib/`), and the `sky` CLI. Out of scope: third-party Go packages
pulled in via `sky add` (report those to their maintainers), and vulnerabilities
that require an already-compromised host or a non-default, explicitly-unsafe
configuration.
