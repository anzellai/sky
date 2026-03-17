#!/bin/bash
# Test that all examples compile successfully.
# Run from the project root: ./scripts/test-examples.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

# Build the compiler first
npm run build --silent 2>/dev/null

PASS=0
FAIL=0
FAILURES=""

for dir in examples/*/; do
    # Skip directories without a src/Main.sky
    [ -f "$dir/src/Main.sky" ] || continue

    name=$(basename "$dir")
    cd "$ROOT/$dir"
    result=$(node "$ROOT/dist/bin/sky.js" build src/Main.sky 2>&1 | tail -1)

    if echo "$result" | grep -q "Build complete"; then
        printf "  \033[32mPASS\033[0m  %s\n" "$name"
        PASS=$((PASS + 1))
    else
        printf "  \033[31mFAIL\033[0m  %s\n" "$name"
        FAIL=$((FAIL + 1))
        FAILURES="$FAILURES  - $name\n"
    fi
done

echo ""
echo "Results: $PASS passed, $FAIL failed"

if [ "$FAIL" -gt 0 ]; then
    printf "\nFailed:\n$FAILURES"
    exit 1
fi
