#!/usr/bin/env bash
# Run the Neovim-headless LSP test suite. Each test exercises a single
# user-visible LSP behaviour (hover / completion / goto-def) end-to-end
# through Neovim's real LSP client — so it catches editor-level bugs
# that synthetic JSON-RPC tests miss (label-vs-insertText, filterText,
# scope handling, etc.).
#
# Usage:  scripts/lsp-test-nvim.sh
#
# Exit code: 0 if all tests pass, non-zero with first failure name.

set -u

PROJECT_DIR="${LSP_NVIM_PROJECT:-/tmp/lsp-real-test}"
mkdir -p "$PROJECT_DIR/src"

# Minimal sky.toml so the LSP can find the project root.
if [ ! -f "$PROJECT_DIR/sky.toml" ]; then
    cat > "$PROJECT_DIR/sky.toml" <<'EOF'
[project]
name = "lsp-real-test"
version = "0.0.0"
EOF
fi

TESTS=(
    hover-task-run
    hover-field
    hover-type-name
    completion-qualified-insert-text
    completion-field
    completion-let-binding
    goto-def-type-name
)

failures=()
for t in "${TESTS[@]}"; do
    out=$(nvim --headless -u NONE -l scripts/lsp-test-nvim.lua "$PROJECT_DIR" "$t" 2>&1)
    result=$(printf '%s' "$out" | grep -oE '(PASS|FAIL): [^"]*' | head -1)
    if [ -z "$result" ]; then
        result="??: $t (no PASS/FAIL marker — check raw output)"
        failures+=("$t")
    elif [[ "$result" == FAIL:* ]]; then
        failures+=("$t")
    fi
    echo "$result"
done

echo ""
if [ ${#failures[@]} -eq 0 ]; then
    echo "All ${#TESTS[@]} tests passed."
    exit 0
else
    echo "FAILED (${#failures[@]} of ${#TESTS[@]}): ${failures[*]}"
    exit 1
fi
