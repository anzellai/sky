#!/usr/bin/env bash
#
# doc-examples.sh — the LIVE-DOCS gate.
#
# Extracts every FULL-MODULE Sky code fence (```elm / ```sky whose first line is
# `module …`) from the live reference docs (docs/, EXCLUDING docs/history/) and
# `sky check`s each in a throwaway project, so a complete example that no longer
# compiles fails here instead of silently rotting. Partial snippets (not a full
# module) are illustrative and skipped by design.
#
# Opt a specific full-module example out with a line
#     -- doc-example: skip
# anywhere in the block (e.g. an intentionally-erroring example).
#
# Usage:  scripts/doc-examples.sh [-v]        (-v prints each check's diagnostic)
# Env:    SKY_BIN=/path/to/sky  (default: sky-out/sky, else `sky` on PATH)
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
source "$ROOT/scripts/lib/with-timeout.sh"
SKY="${SKY_BIN:-$ROOT/sky-out/sky}"
[ -x "$SKY" ] || SKY="$(command -v sky 2>/dev/null || true)"
if [ -z "${SKY:-}" ] || [ ! -x "$SKY" ]; then
  echo "doc-examples: no sky binary (set SKY_BIN or build sky-out/sky)"; exit 2
fi
VERBOSE=0; [ "${1:-}" = "-v" ] && VERBOSE=1

BLOCKS="$(mktemp -d)"; PROJ="$(mktemp -d)"
trap 'rm -rf "$BLOCKS" "$PROJ"' EXIT


# Anti-vacuity floor. `total` is derived from a text scan of docs/, so any
# drift in fence syntax, a docs/ reorg, or a change to the `find` path can
# empty the corpus — and an empty corpus printed "doc-examples: 0/0 …" followed
# by "DOC-EXAMPLES GATE: PASS", exit 0. Demonstrated: a docs tree whose single
# example is deliberately uncompilable passes when its fence carries an info
# string. conformance.sh already guards this way (`ran -eq 0` -> exit 2);
# doc-examples did not. Raise the floor when docs gain examples; never lower it
# to make a red run green.
DOC_EXAMPLES_FLOOR="${DOC_EXAMPLES_FLOOR:-12}"

pass=0; fail=0; total=0
declare -a failures

# Collect live docs (exclude the frozen history tree).
while IFS= read -r md; do
  # Split the doc into full-module fence blocks; write each to BLOCKS/<n>.sky and
  # emit "<file>\t<relpath>\t<startline>" so we can report a precise location.
  rel="${md#"$ROOT"/}"
  awk -v dir="$BLOCKS" -v rel="$rel" '
    BEGIN { n=0 }
    # Accept an info string after the language ("```elm title=Main.sky"), the
    # form mkdocs/docusaurus emit. The old pattern anchored at end-of-line, so
    # adding an attribute to a fence silently removed that example from the
    # corpus — and with the corpus empty the gate still reported PASS.
    /^```(elm|sky)([ \t].*)?$/ { infence=1; first=1; buf=""; ismod=0; skip=0; startln=NR+1; next }
    /^```[ \t]*$/ {
      if (infence && ismod && !skip) {
        n++; f=dir "/" NR "_" n ".sky"; printf "%s", buf > f; close(f)
        print f "\t" rel "\t" startln
      }
      infence=0; next
    }
    infence {
      # Only self-contained RUNNABLE examples: `module Main …` with a `main`.
      # Library / test module snippets (`module Lib.User`, `module FooTest`) are
      # multi-file illustrations that cannot `go build` standalone (no `main`);
      # verifying those is a future enhancement (scaffold + import).
      if (first) { first=0; if ($0 ~ /^module Main[ (]/) ismod=1 }
      if ($0 ~ /doc-example:[ ]*skip/) skip=1
      buf = buf $0 "\n"
    }
  ' "$md"
done < <(find "$ROOT/docs" -name '*.md' | grep -v '/history/' | sort) > "$BLOCKS/index"

# Check each extracted full-module block in a fresh throwaway project.
while IFS=$'\t' read -r file rel startln; do
  [ -f "$file" ] || continue
  total=$((total + 1))
  # module name → last segment drives the source filename + entry
  modname="$(head -1 "$file" | sed -E 's/^module[ ]+([A-Za-z0-9_.]+).*/\1/')"
  last="${modname##*.}"
  # `rm -rf "$PROJ"/*` left the DOTFILES behind — .skycache/ and .skydeps/ —
  # so each example inherited the previous one's resolved deps and lowered
  # cache. An example could compile only because its predecessor had populated
  # the cache. Recreate the project directory outright.
  rm -rf "$PROJ"; mkdir -p "$PROJ/src"
  printf 'name = "docexamples"\nversion = "0.1.0"\nentry = "src/%s.sky"\n' "$last" > "$PROJ/sky.toml"
  cp "$file" "$PROJ/src/$last.sky"
  if out="$( ( cd "$PROJ" && with_timeout 300 "$SKY" check "src/$last.sky" ) 2>&1 )"; then
    pass=$((pass + 1))
    [ "$VERBOSE" = 1 ] && printf '  ok    %s:%s (%s)\n' "$rel" "$startln" "$modname"
  else
    fail=$((fail + 1))
    failures+=("$rel:$startln ($modname)")
    printf '  FAIL  %s:%s (%s)\n' "$rel" "$startln" "$modname"
    if [ "$VERBOSE" = 1 ]; then
      printf '%s\n' "$out" | grep -vE "zoxide|Please ensure|If the issue|github.com/ajeet|Disable this" | tail -8 | sed 's/^/        /'
    fi
  fi
done < "$BLOCKS/index"

echo "------------------------------------------------------------"
echo "doc-examples: $pass/$total full-module doc examples compile"
if [ "$total" -lt "$DOC_EXAMPLES_FLOOR" ]; then
  echo "DOC-EXAMPLES GATE: INCONCLUSIVE — extracted $total example(s), floor is $DOC_EXAMPLES_FLOOR."
  echo "  The gate verifies nothing it cannot find. Either docs/ genuinely lost"
  echo "  examples (lower DOC_EXAMPLES_FLOOR deliberately, in the same commit),"
  echo "  or the fence/scan drifted and the corpus silently emptied."
  exit 2
fi
if [ "$fail" -gt 0 ]; then
  echo "DOC-EXAMPLES GATE: FAIL — $fail example(s) no longer compile:"
  for f in "${failures[@]}"; do echo "  - $f"; done
  echo "(re-run with -v for the diagnostics; fix the doc or add '-- doc-example: skip')"
  exit 1
fi
echo "DOC-EXAMPLES GATE: PASS"
