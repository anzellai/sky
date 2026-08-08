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
SKY="${SKY_BIN:-$ROOT/sky-out/sky}"
[ -x "$SKY" ] || SKY="$(command -v sky 2>/dev/null || true)"
if [ -z "${SKY:-}" ] || [ ! -x "$SKY" ]; then
  echo "doc-examples: no sky binary (set SKY_BIN or build sky-out/sky)"; exit 2
fi
VERBOSE=0; [ "${1:-}" = "-v" ] && VERBOSE=1

BLOCKS="$(mktemp -d)"; PROJ="$(mktemp -d)"
trap 'rm -rf "$BLOCKS" "$PROJ"' EXIT

pass=0; fail=0; total=0
declare -a failures

# Collect live docs (exclude the frozen history tree).
while IFS= read -r md; do
  # Split the doc into full-module fence blocks; write each to BLOCKS/<n>.sky and
  # emit "<file>\t<relpath>\t<startline>" so we can report a precise location.
  rel="${md#"$ROOT"/}"
  awk -v dir="$BLOCKS" -v rel="$rel" '
    BEGIN { n=0 }
    /^```(elm|sky)[ \t]*$/ { infence=1; first=1; buf=""; ismod=0; skip=0; startln=NR+1; next }
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
  rm -rf "$PROJ"/* 2>/dev/null
  mkdir -p "$PROJ/src"
  printf 'name = "docexamples"\nversion = "0.1.0"\nentry = "src/%s.sky"\n' "$last" > "$PROJ/sky.toml"
  cp "$file" "$PROJ/src/$last.sky"
  if out="$( ( cd "$PROJ" && "$SKY" check "src/$last.sky" ) 2>&1 )"; then
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
if [ "$fail" -gt 0 ]; then
  echo "DOC-EXAMPLES GATE: FAIL — $fail example(s) no longer compile:"
  for f in "${failures[@]}"; do echo "  - $f"; done
  echo "(re-run with -v for the diagnostics; fix the doc or add '-- doc-example: skip')"
  exit 1
fi
echo "DOC-EXAMPLES GATE: PASS"
