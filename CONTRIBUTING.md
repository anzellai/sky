# Contributing to Sky

Thanks for your interest in contributing!

## Licence of contributions

By submitting a contribution (pull request, patch, code suggestion,
documentation change, or any other Contribution as defined by the
licence), you agree that your contribution is licensed to the Sky
project under the terms of [Apache License, Version 2.0](LICENSE).

This is the same licence the rest of Sky is distributed under, and
following the standard Apache 2.0 inbound = outbound model: you do
not need to sign a separate Contributor License Agreement (CLA), but
your contribution must be one you have the right to submit under
this licence.

The patent-grant clause (Apache 2.0 §3) means contributors grant Sky
users a perpetual, irrevocable patent licence to any of their patent
claims that are necessarily infringed by their contribution. The
patent-retaliation clause means anyone who initiates patent
litigation against Sky users for the contribution loses that grant.
This is the standard Apache 2.0 mechanic for protecting an open
codebase from patent ambush — please make sure you understand it
before submitting code that is meaningfully novel relative to your
employer's patent portfolio. (If you are contributing on behalf of a
company, get sign-off from someone authorised to grant patent
licences for code you submit.)

## What changes from before

Sky was previously distributed under the MIT licence (releases up to
and including v0.10.0). Existing MIT-licensed releases keep their
original terms — that is how the grant works once issued. The next
release (v0.10.1 onwards) ships under Apache 2.0. Any contribution
landed after the relicense commit is accepted under Apache 2.0.

If you previously contributed code under MIT and you would like that
code to remain MIT-only, please open an issue and we will discuss.
The expectation is that contributors are happy with the more
protective Apache 2.0 terms; the relicense was chosen precisely to
extend protection (patent grant, trademark clause, NOTICE
attribution mechanism) to all users.

## Prior-art attribution and derivative-work files

A small number of files in `src/Sky/` are derivative works adapted
from elm/compiler under BSD-3-Clause (see [NOTICE.md](NOTICE.md) for
the full list and licence text). When modifying those files, please:

- Keep the per-file header that names the upstream module + licence
  + copyright.
- If your changes are substantial enough that the file is no longer
  meaningfully derivative, leave the header in place anyway — the
  attribution costs nothing and protects everyone.

When adding a *new* file that adapts code from another permissive-
licensed project, mirror that header convention (name the upstream
project, its licence, the copyright holder, and point at NOTICE.md)
and add an entry to NOTICE.md in the same PR.

## How to contribute

1. Open an issue first if the change is non-trivial — to align on
   the design before implementation.
2. Fork the repo, create a feature branch, make your changes.
3. Run `cabal test` and `scripts/example-sweep.sh` (full
   end-to-end sweep) — both must be green.
4. Open a PR with a clear description of what you changed and why.
   The PR description should reference any related issue.

For development setup, build instructions, and the testing
philosophy, see [docs/development.md](docs/development.md).

For the project's coding conventions, see the "Core Principles"
section of [CLAUDE.md](CLAUDE.md). British English spelling
throughout. No suppressing type errors. Root-cause fixes only.
