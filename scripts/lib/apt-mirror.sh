# shellcheck shell=bash
#
# apt_mirror_rewrite <file> <url>
#
# Print <file> with every Ubuntu ARCHIVE mirror replaced by <url>. Pure: it
# reads the file and writes the result to stdout, so a caller (or a test)
# decides where the result goes. security.ubuntu.com is left as it is.
#
# Three shapes are handled:
#   * deb822 sources (`URIs: http://azure.archive.ubuntu.com/ubuntu/`),
#   * classic sources.list (`deb http://archive.ubuntu.com/ubuntu noble main`),
#   * an apt MIRRORLIST (`/etc/apt/apt-mirrors.txt`, one URI per line with
#     optional tab-separated metadata), which the sources reach through
#     `URIs: mirror+file:/etc/apt/apt-mirrors.txt`.
#
# The mirrorlist is why this exists. On 2026-10-26 (release run 40338067557,
# gate-web-1) the runner's sources named `mirror+file:/etc/apt/apt-mirrors.txt`,
# and the azure mirror lived only in that list. The old rewrite edited
# ubuntu.sources and sources.list, found no archive URL there, and changed
# nothing: all three attempts of scripts/ci/playwright-deps.sh downloaded from
# azure.archive.ubuntu.com while the log said "apt mirror: <other>", and every
# attempt ran out of time. A mirrorlist only moves to its next entry when a
# download FAILS, never when it is slow, so its own fallback did not help.
# In a mirrorlist every archive entry becomes <url>, so the slow mirror is
# never tried again.
apt_mirror_rewrite() {
  local file="$1" url="$2"
  sed -E \
    -e "s#https?://[a-z0-9.-]*archive\.ubuntu\.com/ubuntu/?#${url}#g" \
    -e "s#https?://mirrors\.edge\.kernel\.org/ubuntu/?#${url}#g" \
    "$file"
}
