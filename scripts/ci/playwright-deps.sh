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
# Each attempt runs as root inside the time limit's process group, so the
# limit stops apt-get and its download workers too. Run through sudo, apt-get
# sat outside that group: on 2026-10-23 (release run 39942166203) a cut attempt
# left apt-get holding the dpkg lock, and both later mirrors failed at once.
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
# shellcheck source=../lib/apt-mirror.sh
source "$here/../lib/apt-mirror.sh"

attempt_secs="${SKY_APT_ATTEMPT_SECS:-600}"
mirrors=(
  ""   # the runner's own mirror, unchanged
  "http://archive.ubuntu.com/ubuntu/"
  "http://mirrors.edge.kernel.org/ubuntu/"
)

# Every file that can name the runner's archive mirror: the deb822 and
# classic sources, and the apt mirrorlist they may point at through
# `mirror+file:` (the runner image does, see scripts/lib/apt-mirror.sh).
# security.ubuntu.com lines are left as they are.
sources=()
for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list /etc/apt/apt-mirrors.txt; do
  [ -f "$f" ] && sources+=("$f")
done

use_mirror() {
  local url="$1" f new
  for f in "${sources[@]}"; do
    new="$(apt_mirror_rewrite "$f" "$url")"
    printf '%s\n' "$new" | sudo tee "$f" >/dev/null
  done
  # A rewrite that changed nothing would retry the same slow mirror while the
  # log claimed another one (release run 40338067557). Say so and stop.
  if ! grep -qF "$url" "${sources[@]}" 2>/dev/null; then
    echo "::error::apt mirror: no apt source names ${url} after the rewrite (${sources[*]:-none found})" >&2
    exit 1
  fi
  echo "apt mirror: ${url}"
}

# The repository's pinned Playwright (npm ci ran before this script).
playwright="${GITHUB_WORKSPACE:-$PWD}/node_modules/.bin/playwright"
if [ ! -x "$playwright" ]; then
  echo "::error::playwright-deps.sh: $playwright is missing; run npm ci first" >&2
  exit 2
fi

# Wait (bounded) until no process holds the dpkg lock, so the next attempt
# does not fail at once on a lock a cut attempt left behind.
wait_for_dpkg_lock() {
  local n
  for n in $(seq 1 30); do
    if ! sudo fuser /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "::warning::the dpkg lock is still held after 60s; stopping apt-get"
  sudo pkill -KILL -x apt-get || true
  sudo pkill -KILL -f '/usr/lib/apt/methods/' || true
  sleep 2
}

for i in "${!mirrors[@]}"; do
  if [ -n "${mirrors[$i]}" ]; then
    use_mirror "${mirrors[$i]}"
  fi
  echo "Installing Playwright system packages (attempt $((i + 1)) of ${#mirrors[@]}, up to ${attempt_secs}s)"
  # As root, Playwright runs apt-get itself (no inner sudo), so every apt
  # process is in the group the limit stops.
  if sudo env "PATH=$PATH" bash -c 'source "$1"; shift; secs="$1"; shift; with_timeout "$secs" "$@"' \
      _ "$here/../lib/with-timeout.sh" "$attempt_secs" "$playwright" install-deps "$@"; then
    exit 0
  fi
  echo "::warning::Playwright system packages: attempt $((i + 1)) failed or ran out of time"
  wait_for_dpkg_lock
  # A cut install can leave dpkg half-configured; finish it before the next try.
  sudo dpkg --configure -a || true
done

echo "::error::Playwright system packages did not install from any mirror (${#mirrors[@]} attempts of ${attempt_secs}s)" >&2
exit 1
