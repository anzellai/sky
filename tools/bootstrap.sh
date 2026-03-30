#!/bin/bash
# Bootstrap script for the Sky compiler
set -e
cd "$(dirname "$0")/.."

echo "=== Sky Compiler Bootstrap ==="

# Step 1: Clear lowered cache and compile Sky → Go
rm -rf .skycache/lowered
bin/sky build src/Main.sky 2>&1 | grep -v "zoxide"

# Step 2: Build Go binary
cd sky-out
go build -gcflags="all=-l" -o ../bin/sky . 2>&1 | grep -v "invalid UTF-8" | grep -v "deprecated" | grep -v "copylocks" | grep -v "zoxide" || true
cd ..

echo "=== Bootstrap complete ==="
bin/sky --version
