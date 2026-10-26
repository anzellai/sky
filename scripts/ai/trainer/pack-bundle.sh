#!/usr/bin/env bash
#
# scripts/ai/trainer/pack-bundle.sh - archive a trainer bundle tree so that the
# same content always gives the same bytes.
#
#   pack-bundle.sh <bundle-dir> <out.tar.gz> [<epoch>]
#
# <epoch> is the time every entry carries (seconds since 1970); it defaults to
# SOURCE_DATE_EPOCH, else the commit time of the tree this script is in. The
# archive is a function of the bundle's names, contents and executable bits
# only:
#
#   * entries in byte order of their names, the top directory first;
#   * owner and group 0, no user or group names;
#   * every mtime is <epoch>; modes are 0755 for directories and executables,
#     0644 for everything else;
#   * ustar format, so no pax headers, extended attributes, ACLs or flags;
#   * gzip -n, so no name and no time in the gzip header.
#
# build-trainer-bundle.sh archives with it, and the workflow repacks a bundle
# it has unpacked and touched, and compares the bytes.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"

[ $# -ge 2 ] || { echo "usage: pack-bundle.sh <bundle-dir> <out.tar.gz> [<epoch>]" >&2; exit 2; }
SRC="$1" OUTF="$2"
EPOCH="${3:-${SOURCE_DATE_EPOCH:-}}"
if [ -z "$EPOCH" ]; then
  EPOCH="$(git -C "$ROOT" log -1 --format=%ct 2>/dev/null || true)"
fi
case "$EPOCH" in '' | *[!0-9]*) echo "pack-bundle: no epoch (pass one, set SOURCE_DATE_EPOCH, or run in a git tree)" >&2; exit 1 ;; esac
[ -d "$SRC" ] || { echo "pack-bundle: $SRC is not a directory" >&2; exit 1; }

SRC="$(cd "$SRC" && pwd)"
PARENT="$(dirname "$SRC")" NAME="$(basename "$SRC")"
mkdir -p "$(dirname "$OUTF")"
OUTF="$(cd "$(dirname "$OUTF")" && pwd)/$(basename "$OUTF")"

# Modes: executable or not, nothing else survives.
find "$SRC" -type d -exec chmod 0755 {} +
find "$SRC" -type f -perm -u+x -exec chmod 0755 {} +
find "$SRC" -type f ! -perm -u+x -exec chmod 0644 {} +
if find "$SRC" ! -type f ! -type d | grep -q .; then
  echo "pack-bundle: $SRC holds something that is neither a file nor a directory:" >&2
  find "$SRC" ! -type f ! -type d >&2
  exit 1
fi

LIST="$(mktemp "${TMPDIR:-/tmp}/pack-bundle.XXXXXX")"
trap 'rm -f "$LIST"' EXIT
(cd "$PARENT" && find "$NAME" -print | LC_ALL=C sort > "$LIST")

if tar --version 2>/dev/null | grep -q 'GNU tar'; then
  tar -C "$PARENT" --format=ustar --owner=0 --group=0 --numeric-owner \
    --mtime="@$EPOCH" --no-recursion -T "$LIST" -cf - | gzip -n -9 > "$OUTF"
else
  # bsdtar (macOS): no --mtime, so the tree's mtimes are set first.
  stamp="$(perl -MPOSIX=strftime -e 'print strftime("%Y%m%d%H%M.%S", gmtime($ARGV[0]))' "$EPOCH")"
  (cd "$PARENT" && while IFS= read -r f; do TZ=UTC touch -h -t "$stamp" "$f"; done < "$LIST")
  COPYFILE_DISABLE=1 tar -C "$PARENT" --format=ustar --uid 0 --gid 0 --uname '' --gname '' \
    --no-xattrs --no-acls --no-fflags --no-recursion -T "$LIST" -cf - | gzip -n -9 > "$OUTF"
fi
echo "$OUTF"
