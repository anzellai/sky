#!/usr/bin/env bash
#
# Make sure each named command-line tool is on PATH, installing the apt package
# of the same name only when it is absent.
#
# The runner images differ in what they preinstall (Ubuntu 24.04 and 26.04 both
# ship sqlite3 and brotli today, but nothing promises that), so a job states
# the tools it needs and this installs only the gap. A tool that is still
# absent afterwards FAILS the job with an ::error::. It never skips.
#
# Lookup is by PATH (`type -P`), never a shell builtin or keyword: `time`
# must find /usr/bin/time, not bash's `time` keyword.
#
# Usage: bash scripts/ci/ensure-tools.sh sqlite3 brotli ...
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "::error::ensure-tools.sh: name at least one tool" >&2
  exit 2
fi

absent=()
for tool in "$@"; do
  type -P "$tool" >/dev/null 2>&1 || absent+=("$tool")
done

if [ "${#absent[@]}" -gt 0 ]; then
  echo "Installing absent tools: ${absent[*]}"
  sudo apt-get update -qq
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${absent[@]}"
fi

for tool in "$@"; do
  if ! type -P "$tool" >/dev/null 2>&1; then
    echo "::error::$tool is not on PATH after install" >&2
    exit 1
  fi
  echo "$tool: $(type -P "$tool")"
done
