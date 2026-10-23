#!/usr/bin/env bash
#
# Install the system packages Playwright's browsers need, bounded in time, and
# move to another Ubuntu mirror when the current one is too slow.
#
# Why this exists: on 2026-10-23 the runner's default mirror
# (azure.archive.ubuntu.com) sent ~190 packages in 40 minutes, three runs in a
# row (release runs 39916275663, 39920683474 and 39930892523). The download
# never failed, it was only slow, so apt's own retries never fired, and the
# gate-web-* jobs ran out of time before a single browser test had run.
#
# Each attempt gets SKY_APT_ATTEMPT_SECS (default 600; a normal install takes
# about 2 minutes). When an attempt runs out of time, the next attempt uses the
# next mirror. Packages an attempt already downloaded stay in apt's cache, so
# the next attempt does not fetch them again. If every mirror fails, the job
# FAILS with an ::error::. It never skips.
#
# Usage: bash scripts/ci/playwright-deps.sh chromium webkit
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "::error::playwright-deps.sh: name at least one browser" >&2
  exit 2
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib/with-timeout.sh
source "$here/../lib/with-timeout.sh"

attempt_secs="${SKY_APT_ATTEMPT_SECS:-600}"
mirrors=(
  ""   # the runner's own mirror, unchanged
  "http://archive.ubuntu.com/ubuntu/"
  "http://mirrors.edge.kernel.org/ubuntu/"
)

# The archive mirror lines in the runner's apt sources (deb822 and classic).
# security.ubuntu.com lines are left as they are.
sources=()
for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list; do
  [ -f "$f" ] && sources+=("$f")
done

use_mirror() {
  local url="$1" f
  for f in "${sources[@]}"; do
    sudo sed -E -i "s#https?://[a-z0-9.-]*archive\.ubuntu\.com/ubuntu/?#${url}#g; s#https?://mirrors\.edge\.kernel\.org/ubuntu/?#${url}#g" "$f"
  done
  echo "apt mirror: ${url}"
}

for i in "${!mirrors[@]}"; do
  if [ -n "${mirrors[$i]}" ]; then
    use_mirror "${mirrors[$i]}"
  fi
  echo "Installing Playwright system packages (attempt $((i + 1)) of ${#mirrors[@]}, up to ${attempt_secs}s)"
  if with_timeout "$attempt_secs" npx playwright install-deps "$@"; then
    exit 0
  fi
  echo "::warning::Playwright system packages: attempt $((i + 1)) failed or ran out of time"
  # A cut install can leave dpkg half-configured; finish it before the next try.
  sudo dpkg --configure -a || true
done

echo "::error::Playwright system packages did not install from any mirror (${#mirrors[@]} attempts of ${attempt_secs}s)" >&2
exit 1
